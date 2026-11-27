//go:build linux || darwin || freebsd || dragonfly

package diskreserve

import "golang.org/x/sys/unix"

// statfs measures the filesystem holding path with statfs(2).
func statfs(path string) (Capacity, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return Capacity{}, err
	}
	blockSize := int64(stat.Bsize)
	return Capacity{
		TotalBytes:  int64(stat.Blocks) * blockSize,
		FreeBytes:   int64(stat.Bavail) * blockSize,
		TotalInodes: int64(stat.Files),
		FreeInodes:  int64(stat.Ffree),
	}, nil
}
