package diskreserve

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeGuard returns a guard over a fake filesystem with the given free bytes.
func fakeGuard(totalBytes int64, freeBytes int64) *Guard {
	guard, err := NewGuard(&NewGuardArgs{
		Path: "/",
		Measure: func(string) (Capacity, error) {
			return Capacity{
				TotalBytes:  totalBytes,
				FreeBytes:   freeBytes,
				TotalInodes: 1_000_000,
				FreeInodes:  900_000,
			}, nil
		},
	})
	if err != nil {
		panic(err)
	}
	return guard
}

// TestThresholds checks the reserve clamp and the three levels.
func TestThresholds(t *testing.T) {
	const gibibyte int64 = 1024 * 1024 * 1024
	cases := []struct {
		total   int64
		free    int64
		reserve int64
		level   Level
	}{
		{total: 10 * gibibyte, free: 9 * gibibyte, reserve: 2 * gibibyte, level: LevelNormal},
		{total: 100 * gibibyte, free: 9 * gibibyte, reserve: 10 * gibibyte, level: LevelReserve},
		{total: 100 * gibibyte, free: 2 * gibibyte, reserve: 10 * gibibyte, level: LevelCritical},
		{total: 1000 * gibibyte, free: 60 * gibibyte, reserve: 50 * gibibyte, level: LevelNormal},
	}
	for _, testCase := range cases {
		status, err := fakeGuard(testCase.total, testCase.free).Status()
		if err != nil {
			t.Fatal(err)
		}
		if status.ReserveBytes != testCase.reserve || status.Level != testCase.level {
			t.Errorf(
				"total %d free %d: reserve %d level %s",
				testCase.total,
				testCase.free,
				status.ReserveBytes,
				status.Level,
			)
		}
	}
	guard := fakeGuard(100*gibibyte, 11*gibibyte)
	if err := guard.Require(PriorityLow, 2*gibibyte); !errors.Is(err, ErrBelowReserve) {
		t.Fatal("low-priority write past the reserve admitted")
	}
	if err := guard.Require(PriorityHigh, 2*gibibyte); err != nil {
		t.Fatal("high-priority write inside the reserve refused")
	}
	if err := guard.Require(PriorityHigh, 9*gibibyte); !errors.Is(err, ErrCritical) {
		t.Fatal("high-priority write into the last quarter of the reserve admitted")
	}
}

// TestReservationsAddUp reserves twice on a disk where each reservation
// fits alone but not both: the second is refused, a released reservation
// frees its bytes, and Status reports what is held.
func TestReservationsAddUp(t *testing.T) {
	const gibibyte int64 = 1024 * 1024 * 1024
	// Reserve is 10 GiB; 16 GiB free leaves 6 GiB above it.
	guard := fakeGuard(100*gibibyte, 16*gibibyte)
	first, err := guard.Reserve(&ReserveArgs{Priority: PriorityLow, Bytes: 4 * gibibyte})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Reserve(&ReserveArgs{Priority: PriorityLow, Bytes: 4 * gibibyte}); !errors.Is(
		err,
		ErrBelowReserve,
	) {
		t.Fatalf("a second low-priority reservation past the reserve: %v", err)
	}
	if err := guard.Require(PriorityLow, 4*gibibyte); !errors.Is(err, ErrBelowReserve) {
		t.Fatalf("Require ignored a reservation: %v", err)
	}
	status, err := guard.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.ReservedBytes != 4*gibibyte {
		t.Fatalf("Status reports %d reserved bytes, want %d", status.ReservedBytes, 4*gibibyte)
	}
	first.Release()
	first.Release()
	second, err := guard.Reserve(&ReserveArgs{Priority: PriorityLow, Bytes: 4 * gibibyte})
	if err != nil {
		t.Fatalf("a reservation after a release: %v", err)
	}
	second.Release()
	status, err = guard.Status()
	if err != nil || status.ReservedBytes != 0 {
		t.Fatalf("after releasing everything: %+v, %v", status, err)
	}
	if err := first.Grow(1, 0); err == nil {
		t.Fatal("a released reservation grew")
	}
}

// TestReservationGrowThresholds grows reservations of each priority: a low
// one stops at the reserve, a high one at a quarter of it, and an exempt
// one never.
func TestReservationGrowThresholds(t *testing.T) {
	const gibibyte int64 = 1024 * 1024 * 1024
	guard := fakeGuard(100*gibibyte, 16*gibibyte)
	low, err := guard.Reserve(&ReserveArgs{Priority: PriorityLow, Bytes: gibibyte})
	if err != nil {
		t.Fatal(err)
	}
	if err := low.Grow(6*gibibyte, 0); !errors.Is(err, ErrBelowReserve) {
		t.Fatalf("a low-priority reservation grew past the reserve: %v", err)
	}
	if err := low.Grow(4*gibibyte, 0); err != nil {
		t.Fatalf("a low-priority reservation that still fits: %v", err)
	}
	low.Release()
	high, err := guard.Reserve(&ReserveArgs{Priority: PriorityHigh, Bytes: 10 * gibibyte})
	if err != nil {
		t.Fatalf("a high-priority reservation into the reserve: %v", err)
	}
	if err := high.Grow(4*gibibyte, 0); !errors.Is(err, ErrCritical) {
		t.Fatalf("a high-priority reservation grew into the last quarter of the reserve: %v", err)
	}
	exempt, err := guard.Reserve(&ReserveArgs{Priority: PriorityExempt, Bytes: 100 * gibibyte})
	if err != nil {
		t.Fatalf("an exempt reservation was refused: %v", err)
	}
	if err := exempt.Grow(100*gibibyte, 1_000_000); err != nil {
		t.Fatalf("an exempt reservation could not grow: %v", err)
	}
	if err := guard.Require(PriorityHigh, 0); !errors.Is(err, ErrCritical) {
		t.Fatalf("a high-priority write past what exempt reservations hold: %v", err)
	}
	exempt.Release()
	high.Release()
	if err := guard.Require(PriorityHigh, -5); err != nil {
		t.Fatalf("a negative length: %v", err)
	}
	if _, err := guard.Reserve(&ReserveArgs{Priority: "other"}); err == nil {
		t.Fatal("an unknown priority was reserved")
	}
}

// TestConcurrentReservations has 100 goroutines reserve and release at once
// under -race: no more than fit are held together, and all are returned.
func TestConcurrentReservations(t *testing.T) {
	const gibibyte int64 = 1024 * 1024 * 1024
	// 6 GiB above the reserve fits six reservations of 1 GiB.
	guard := fakeGuard(100*gibibyte, 16*gibibyte)
	var reserved atomic.Int64
	var mostReserved atomic.Int64
	var reservers sync.WaitGroup
	for range 100 {
		reservers.Go(func() {
			reservation, err := guard.Reserve(
				&ReserveArgs{Priority: PriorityLow, Bytes: gibibyte, Inodes: 1},
			)
			if errors.Is(err, ErrBelowReserve) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			now := reserved.Add(1)
			for {
				most := mostReserved.Load()
				if now <= most || mostReserved.CompareAndSwap(most, now) {
					break
				}
			}
			reserved.Add(-1)
			reservation.Release()
		})
	}
	reservers.Wait()
	if most := mostReserved.Load(); most > 6 {
		t.Fatalf("%d reservations of 1 GiB held at once above 6 GiB of room", most)
	}
	status, err := guard.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.ReservedBytes != 0 || status.ReservedInodes != 0 {
		t.Fatalf(
			"%d bytes and %d inodes still reserved",
			status.ReservedBytes,
			status.ReservedInodes,
		)
	}
}

// TestStatfs measures a real filesystem.
func TestStatfs(t *testing.T) {
	guard, err := NewGuard(&NewGuardArgs{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	status, err := guard.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.TotalBytes <= 0 || status.FreeBytes < 0 || status.FreeBytes > status.TotalBytes {
		t.Fatalf("implausible capacity %+v", status.Capacity)
	}
}

// TestNewGuardArguments checks the configured reserve and the refusals.
func TestNewGuardArguments(t *testing.T) {
	if _, err := NewGuard(&NewGuardArgs{}); err == nil {
		t.Fatal("a guard without a path")
	}
	if _, err := NewGuard(&NewGuardArgs{Path: "/", MinimumReserveBytes: 10, MaximumReserveBytes: 5}); err == nil {
		t.Fatal("a minimum reserve above its maximum")
	}
	guard, err := NewGuard(&NewGuardArgs{
		Path:         "/",
		ReserveBytes: 7,
		Measure: func(string) (Capacity, error) {
			return Capacity{TotalBytes: 100, FreeBytes: 50}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := guard.Status()
	if err != nil || status.ReserveBytes != 7 || status.ReserveInodes != 0 || status.Level != LevelNormal {
		t.Fatalf("status %+v, %v", status, err)
	}
	if err := guard.Require("other", 1); err == nil {
		t.Fatal("an unknown priority was checked")
	}
}

// fixedGuard returns a guard over a fake filesystem with the given capacity
// and byte reserve.
func fixedGuard(t *testing.T, capacity Capacity, reserveBytes int64) *Guard {
	t.Helper()
	guard, err := NewGuard(&NewGuardArgs{
		Path:         "/",
		ReserveBytes: reserveBytes,
		Measure:      func(string) (Capacity, error) { return capacity, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

// TestNoInodeLimit checks that a filesystem reporting no inode limit, such
// as btrfs, admits inode reservations on its bytes alone.
func TestNoInodeLimit(t *testing.T) {
	guard := fixedGuard(t, Capacity{TotalBytes: 100 << 30, FreeBytes: 50 << 30}, 0)
	reservation, err := guard.Reserve(&ReserveArgs{Priority: PriorityLow, Bytes: 1 << 20, Inodes: 1})
	if err != nil {
		t.Fatalf("an inode reservation without an inode limit: %v", err)
	}
	reservation.Release()
}

// TestHugeReservationsStayExact checks that a reservation too large to count
// is refused, so releasing reservations returns exactly what they held.
func TestHugeReservationsStayExact(t *testing.T) {
	guard := fixedGuard(t, Capacity{TotalBytes: 1000, FreeBytes: 100}, 10)
	first, err := guard.Reserve(&ReserveArgs{Priority: PriorityExempt, Bytes: math.MaxInt64})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Reserve(&ReserveArgs{Priority: PriorityExempt, Bytes: 1}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a reservation past the largest count: %v", err)
	}
	if err := first.Grow(1, 0); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("growing past the largest count: %v", err)
	}
	if _, err := guard.Reserve(&ReserveArgs{Priority: PriorityLow, Bytes: math.MaxInt64}); err == nil {
		t.Fatal("a huge low-priority write past a huge exempt one was admitted")
	}
	if err := guard.Require(PriorityHigh, 1); !errors.Is(err, ErrCritical) {
		t.Fatalf("a high-priority write with everything reserved: %v", err)
	}
	second, err := guard.Reserve(&ReserveArgs{Priority: PriorityExempt, Bytes: 0})
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	second.Release()
	status, err := guard.Status()
	if err != nil || status.ReservedBytes != 0 {
		t.Fatalf("after every release %d bytes are reserved: %v", status.ReservedBytes, err)
	}
}

// TestReservePercentRounding checks that the percentage is taken of the
// whole size, not of a truncated hundredth of it.
func TestReservePercentRounding(t *testing.T) {
	guard, err := NewGuard(&NewGuardArgs{
		Path:                "/",
		ReservePercent:      10,
		MinimumReserveBytes: 1,
		MaximumReserveBytes: 100,
		Measure:             func(string) (Capacity, error) { return Capacity{TotalBytes: 199, FreeBytes: 199}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := guard.Status()
	if err != nil || status.ReserveBytes != 19 {
		t.Fatalf("10%% of 199 bytes reserved %d, want 19: %v", status.ReserveBytes, err)
	}
	for _, percent := range []int64{-1, 101} {
		if _, err := NewGuard(&NewGuardArgs{Path: "/", ReservePercent: percent}); err == nil {
			t.Errorf("a reserve of %d%% was accepted", percent)
		}
	}
}
