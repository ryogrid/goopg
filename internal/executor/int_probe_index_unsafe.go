package executor

import "unsafe"

// mapHeaderPtr is the address of m's runtime header: a map value is a single
// pointer to it, so two map values with the same header are the same map.
func mapHeaderPtr(m map[int64][]Row) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&m))
}
