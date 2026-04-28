package storage

import "unsafe"

// unsafePointerOf returns a *byte's address as an unsafe.Pointer. Used
// only for the arena alignment fallback path (see arena.go); no other
// part of the package uses unsafe.
func unsafePointerOf(p *byte) unsafe.Pointer {
	return unsafe.Pointer(p)
}
