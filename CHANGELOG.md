# Changelog

This module follows semantic versioning. Before v1, a minor version may
change the API; each such change says what callers must do.

## v0.2.1 (2026-11-28)

- The README says the module requires Go 1.27.
- Tagged so the module proxy serves this release; v0.2.0 is the same code.

## v0.2.0 (2026-11-28)

- Requires Go 1.27, the newest Go release; Go 1.26 is no longer
  supported.
- CI runs staticcheck and govulncheck at the versions pinned in
  `tools/go.mod`, a module of its own that importers never see.

## v0.1.1 (2026-11-27)

- Every exported default, `Capacity` and `Status` field, and argument has
  its own doc comment with its unit and default. No API change.
- CI also checks formatting, `go mod tidy -diff`, and `govulncheck`.

## v0.1.0 (2026-11-27)

First release.

- `Guard` keeps a reserve of free space and inodes on one filesystem: 10%
  by default, clamped to [2 GiB, 50 GiB] and [10,000, 1,000,000] inodes.
- Writes are admitted by priority: `PriorityLow` is refused below the
  reserve, `PriorityHigh` only below a quarter of it, and `PriorityExempt`
  never, though it is counted against the others.
- `Reserve` holds space for a write in flight, so concurrent writers that
  each fit cannot together run past the reserve.
