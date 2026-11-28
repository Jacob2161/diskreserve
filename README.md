# diskreserve

[![Go Reference](https://pkg.go.dev/badge/github.com/jacob2161/diskreserve.svg)](https://pkg.go.dev/github.com/jacob2161/diskreserve)
[![CI](https://github.com/jacob2161/diskreserve/actions/workflows/ci.yml/badge.svg)](https://github.com/jacob2161/diskreserve/actions/workflows/ci.yml)

Keep a reserve of free disk space and inodes: admit writes by priority as a filesystem fills, and
hold reservations for writes in flight so concurrent writers cannot together run past the reserve.

```bash
go get github.com/jacob2161/diskreserve
```

## Usage

```go
guard, err := diskreserve.NewGuard(&diskreserve.NewGuardArgs{Path: "/var/lib/app"})

upload, err := guard.Reserve(&diskreserve.ReserveArgs{
	Priority: diskreserve.PriorityLow, // refused below the reserve
	Bytes:    contentLength,
})
if errors.Is(err, diskreserve.ErrBelowReserve) {
	http.Error(w, "the server is low on disk space", http.StatusInsufficientStorage)
	return
}
if err != nil {
	http.Error(w, "the disk could not be measured", http.StatusInternalServerError)
	return
}
defer upload.Release()
```

- The reserve is 10% of the filesystem clamped to [2 GiB, 50 GiB], and 10% of its inodes clamped to
  [10,000, 1,000,000]; every bound is configurable.
- Three priorities: `PriorityLow` (new data, refused below the reserve), `PriorityHigh` (refused
  only below a quarter of it), and `PriorityExempt` (never refused, still counted, for writes that
  free space later).
- Thresholds are recomputed on every check, so a grown filesystem recovers by itself. A filesystem
  that reports no inode limit (btrfs) is judged on bytes alone.
- Measures with statfs on Linux, macOS, FreeBSD, and DragonFly; elsewhere, pass your own `Measure`.

Requires Go 1.27. Licensed under the MIT license.
