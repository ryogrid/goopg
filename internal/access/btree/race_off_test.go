//go:build !race

package btree

// raceEnabled is false when the race detector is NOT compiled in.
const raceEnabled = false
