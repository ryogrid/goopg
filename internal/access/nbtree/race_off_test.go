//go:build !race

package nbtree

// raceEnabled is false when the race detector is NOT compiled in.
const raceEnabled = false
