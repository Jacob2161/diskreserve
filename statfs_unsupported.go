//go:build !(linux || darwin || freebsd || dragonfly)

package diskreserve

import "errors"

// statfs is unavailable on this system; pass NewGuardArgs.Measure instead.
func statfs(string) (Capacity, error) {
	return Capacity{}, errors.New("statfs is unavailable on this system; set NewGuardArgs.Measure")
}
