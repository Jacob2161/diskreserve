// Package diskreserve keeps a reserve of free disk space and inodes on one
// filesystem, and admits writes by priority as it fills.
//
// The reserve defaults to 10% of the filesystem, clamped to [2 GiB, 50 GiB],
// and 10% of its inodes, clamped to [10,000, 1,000,000]. Below the reserve
// the Guard is at LevelReserve, and below a quarter of it at LevelCritical:
//
//   - PriorityLow writes, such as new data a client can send later, are
//     refused from LevelReserve on with ErrBelowReserve.
//   - PriorityHigh writes, such as changes to data already stored, are
//     refused only at LevelCritical, with ErrCritical.
//   - PriorityExempt writes, such as a staging file that frees space once it
//     is uploaded elsewhere, are never refused but are counted against the
//     others.
//
// The thresholds are recomputed on every check, so a filesystem that grows
// recovers by itself. A write in flight holds a Reservation for what it will
// add, from before its first byte until it lands, so concurrent writers that
// each fit cannot together run past the reserve.
package diskreserve

import (
	"errors"
	"fmt"
	"math"
	"sync"
)

// Default reserve bounds.
const (
	DefaultReservePercent       = 10
	DefaultMinimumReserveBytes  = 2 << 30
	DefaultMaximumReserveBytes  = 50 << 30
	DefaultMinimumReserveInodes = 10_000
	DefaultMaximumReserveInodes = 1_000_000
)

// Level is the current pressure on the filesystem.
type Level string

const (
	// LevelNormal admits every write.
	LevelNormal Level = "normal"
	// LevelReserve refuses PriorityLow writes.
	LevelReserve Level = "reserve"
	// LevelCritical also refuses PriorityHigh writes.
	LevelCritical Level = "critical"
)

var (
	// ErrTooLarge refuses a reservation that would bring the bytes or inodes
	// reserved past the largest int64.
	ErrTooLarge = errors.New("reservation too large to count")
	// ErrBelowReserve refuses a PriorityLow write that would leave less free
	// than the reserve.
	ErrBelowReserve = errors.New("write would leave less free than the reserve")
	// ErrCritical refuses a PriorityHigh write that would leave less free than
	// a quarter of the reserve.
	ErrCritical = errors.New("write would leave less free than a quarter of the reserve")
)

// Priority decides the level at which a write is refused.
type Priority string

const (
	// PriorityLow is refused from LevelReserve on, with ErrBelowReserve.
	PriorityLow Priority = "low"
	// PriorityHigh is refused at LevelCritical, with ErrCritical.
	PriorityHigh Priority = "high"
	// PriorityExempt is never refused, but is counted against the others.
	PriorityExempt Priority = "exempt"
)

// Capacity is one measurement of a filesystem. Free counts what an
// unprivileged process may use.
type Capacity struct {
	TotalBytes  int64
	FreeBytes   int64
	TotalInodes int64
	FreeInodes  int64
}

// Measure measures the filesystem holding path.
type Measure func(path string) (Capacity, error)

// Status is one measurement of the filesystem, with its reserve and the
// reservations held.
type Status struct {
	Capacity
	Level         Level
	ReserveBytes  int64
	ReserveInodes int64
	// ReservedBytes and ReservedInodes are held by writes in flight; the
	// level counts them as used.
	ReservedBytes  int64
	ReservedInodes int64
}

// NewGuardArgs configures NewGuard.
type NewGuardArgs struct {
	// Path is any path on the measured filesystem. Required.
	Path string
	// ReserveBytes, when positive, replaces the computed byte reserve.
	ReserveBytes int64
	// ReservePercent is the share of the filesystem reserved, before the
	// clamps, 1 to 100. Zero uses DefaultReservePercent.
	ReservePercent int64
	// MinimumReserveBytes and MaximumReserveBytes clamp the byte reserve.
	// Zero or negative uses the defaults.
	MinimumReserveBytes int64
	MaximumReserveBytes int64
	// MinimumReserveInodes and MaximumReserveInodes clamp the inode reserve.
	// Zero or negative uses the defaults.
	MinimumReserveInodes int64
	MaximumReserveInodes int64
	// Measure replaces the statfs system call, as in tests. Nil uses statfs.
	Measure Measure
}

// Guard measures one filesystem and admits writes that leave its reserve
// intact. It is safe for concurrent use.
type Guard struct {
	path                 string
	reserveBytes         int64
	reservePercent       int64
	minimumReserveBytes  int64
	maximumReserveBytes  int64
	minimumReserveInodes int64
	maximumReserveInodes int64
	measure              Measure

	// mutex guards the reserved totals, and is held from a measurement to
	// the reservation it admits.
	mutex          sync.Mutex
	reservedBytes  int64
	reservedInodes int64
}

// NewGuard returns a guard for the filesystem holding args.Path.
func NewGuard(args *NewGuardArgs) (*Guard, error) {
	if args == nil || args.Path == "" {
		return nil, errors.New("no path")
	}
	guard := &Guard{
		path:                 args.Path,
		reserveBytes:         args.ReserveBytes,
		reservePercent:       positiveOr(args.ReservePercent, DefaultReservePercent),
		minimumReserveBytes:  positiveOr(args.MinimumReserveBytes, DefaultMinimumReserveBytes),
		maximumReserveBytes:  positiveOr(args.MaximumReserveBytes, DefaultMaximumReserveBytes),
		minimumReserveInodes: positiveOr(args.MinimumReserveInodes, DefaultMinimumReserveInodes),
		maximumReserveInodes: positiveOr(args.MaximumReserveInodes, DefaultMaximumReserveInodes),
		measure:              args.Measure,
	}
	if guard.measure == nil {
		guard.measure = statfs
	}
	if args.ReservePercent < 0 || args.ReservePercent > 100 {
		return nil, fmt.Errorf("reserve percent %d is not 1 to 100", args.ReservePercent)
	}
	if guard.minimumReserveBytes > guard.maximumReserveBytes ||
		guard.minimumReserveInodes > guard.maximumReserveInodes {
		return nil, errors.New("a minimum reserve is above its maximum")
	}
	return guard, nil
}

func positiveOr(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

// Status measures the filesystem now. Its level counts the bytes and inodes
// reserved for writes still in flight.
func (g *Guard) Status() (*Status, error) {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	return g.statusLocked()
}

// statusLocked is Status for a caller holding g.mutex.
func (g *Guard) statusLocked() (*Status, error) {
	capacity, err := g.measure(g.path)
	if err != nil {
		return nil, fmt.Errorf("measuring %s: %w", g.path, err)
	}
	status := &Status{
		Capacity:       capacity,
		ReservedBytes:  g.reservedBytes,
		ReservedInodes: g.reservedInodes,
	}
	status.ReserveBytes = g.reserveBytes
	if status.ReserveBytes <= 0 {
		status.ReserveBytes = min(
			max(percentOf(capacity.TotalBytes, g.reservePercent), g.minimumReserveBytes),
			g.maximumReserveBytes,
		)
	}
	status.ReserveInodes = min(
		max(percentOf(capacity.TotalInodes, g.reservePercent), g.minimumReserveInodes),
		g.maximumReserveInodes,
	)
	if capacity.TotalInodes == 0 {
		// Filesystems such as btrfs report no inode limit.
		status.ReserveInodes = 0
	}
	status.Level = pressureLevel(status, 0, 0)
	return status, nil
}

// percentOf is percent of total, rounded down, without overflowing or
// truncating total before multiplying. percent is 1 to 100.
func percentOf(total, percent int64) int64 {
	return total/100*percent + total%100*percent/100
}

// saturatingAdd adds non-negative a and b, stopping at the largest int64.
func saturatingAdd(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// pressureLevel classifies free space after the reserved bytes and inodes,
// and incoming ones, are written. A filesystem that reports no inode limit
// is never under inode pressure.
func pressureLevel(status *Status, incomingBytes int64, incomingInodes int64) Level {
	free := status.FreeBytes - saturatingAdd(status.ReservedBytes, max(incomingBytes, 0))
	freeInodes := int64(math.MaxInt64)
	if status.TotalInodes > 0 {
		freeInodes = status.FreeInodes - saturatingAdd(status.ReservedInodes, max(incomingInodes, 0))
	}
	switch {
	case free < status.ReserveBytes/4 || freeInodes < status.ReserveInodes/4:
		return LevelCritical
	case free < status.ReserveBytes || freeInodes < status.ReserveInodes:
		return LevelReserve
	}
	return LevelNormal
}

// deniedError returns the error a write of priority gets at level, or nil.
func deniedError(priority Priority, level Level) error {
	switch {
	case priority == PriorityLow && level != LevelNormal:
		return ErrBelowReserve
	case priority == PriorityHigh && level == LevelCritical:
		return ErrCritical
	}
	return nil
}

func validPriority(priority Priority) error {
	switch priority {
	case PriorityLow, PriorityHigh, PriorityExempt:
		return nil
	}
	return fmt.Errorf("unknown priority %q", priority)
}

// Require checks that a write of priority adding bytes would be admitted now,
// counting what is reserved. It reserves nothing; see Reserve. Negative bytes
// count as 0.
func (g *Guard) Require(priority Priority, bytes int64) error {
	if err := validPriority(priority); err != nil {
		return err
	}
	status, err := g.Status()
	if err != nil {
		return err
	}
	return deniedError(priority, pressureLevel(status, bytes, 0))
}

// ReserveArgs configures Reserve.
type ReserveArgs struct {
	// Priority decides the level that refuses the reservation. Required.
	Priority Priority
	// Bytes and Inodes are the most the write will add; negative counts as 0.
	Bytes  int64
	Inodes int64
}

// Reservation holds bytes and inodes of the filesystem for one write in
// flight. Release it once the write landed or failed.
type Reservation struct {
	guard    *Guard
	priority Priority
	bytes    int64
	inodes   int64
	released bool
}

// Reserve measures the filesystem, subtracts what is already reserved,
// checks the priority's level with args.Bytes and args.Inodes added, and
// reserves them, all under one lock.
func (g *Guard) Reserve(args *ReserveArgs) (*Reservation, error) {
	if args == nil {
		return nil, errors.New("no reservation arguments")
	}
	if err := validPriority(args.Priority); err != nil {
		return nil, err
	}
	reservation := &Reservation{guard: g, priority: args.Priority}
	if err := reservation.Grow(args.Bytes, args.Inodes); err != nil {
		return nil, err
	}
	return reservation, nil
}

// Grow adds bytes and inodes to the reservation when the filesystem still
// admits them at the reservation's priority; refused, the reservation keeps
// what it held. Negative amounts count as 0. Growing a released reservation
// is an error.
func (r *Reservation) Grow(bytes int64, inodes int64) error {
	bytes, inodes = max(bytes, 0), max(inodes, 0)
	guard := r.guard
	guard.mutex.Lock()
	defer guard.mutex.Unlock()
	if r.released {
		return errors.New("growing a released reservation")
	}
	// Totals stay exact, so releases return exactly what was reserved: a
	// reservation too large to count is refused rather than saturated.
	if bytes > math.MaxInt64-guard.reservedBytes || inodes > math.MaxInt64-guard.reservedInodes {
		return ErrTooLarge
	}
	if r.priority != PriorityExempt {
		status, err := guard.statusLocked()
		if err != nil {
			return err
		}
		if err := deniedError(r.priority, pressureLevel(status, bytes, inodes)); err != nil {
			return err
		}
	}
	r.bytes += bytes
	r.inodes += inodes
	guard.reservedBytes += bytes
	guard.reservedInodes += inodes
	return nil
}

// Release returns the reservation's bytes and inodes. Releasing twice, or a
// nil reservation, does nothing.
func (r *Reservation) Release() {
	if r == nil {
		return
	}
	guard := r.guard
	guard.mutex.Lock()
	defer guard.mutex.Unlock()
	if r.released {
		return
	}
	r.released = true
	guard.reservedBytes -= r.bytes
	guard.reservedInodes -= r.inodes
}
