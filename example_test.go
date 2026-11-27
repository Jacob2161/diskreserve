package diskreserve_test

import (
	"errors"
	"fmt"
	"log"
	"testing"

	"github.com/jacob2161/diskreserve"
)

// fixedCapacity is a 100 GiB filesystem with 16 GiB free, so its 10 GiB
// reserve leaves 6 GiB for low-priority writes.
func fixedCapacity(string) (diskreserve.Capacity, error) {
	return diskreserve.Capacity{TotalBytes: 100 << 30, FreeBytes: 16 << 30}, nil
}

func ExampleGuard_Reserve() {
	guard, err := diskreserve.NewGuard(&diskreserve.NewGuardArgs{Path: "/var/lib/app", Measure: fixedCapacity})
	if err != nil {
		log.Fatal(err)
	}
	upload, err := guard.Reserve(&diskreserve.ReserveArgs{Priority: diskreserve.PriorityLow, Bytes: 4 << 30})
	if err != nil {
		log.Fatal(err)
	}
	defer upload.Release()

	_, err = guard.Reserve(&diskreserve.ReserveArgs{Priority: diskreserve.PriorityLow, Bytes: 4 << 30})
	fmt.Println(errors.Is(err, diskreserve.ErrBelowReserve))
	fmt.Println(guard.Require(diskreserve.PriorityHigh, 4<<30))
	// Output:
	// true
	// <nil>
}

func BenchmarkReserveRelease(b *testing.B) {
	guard, err := diskreserve.NewGuard(&diskreserve.NewGuardArgs{Path: "/", Measure: fixedCapacity})
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		reservation, err := guard.Reserve(&diskreserve.ReserveArgs{Priority: diskreserve.PriorityHigh, Bytes: 1 << 20})
		if err != nil {
			b.Fatal(err)
		}
		reservation.Release()
	}
}
