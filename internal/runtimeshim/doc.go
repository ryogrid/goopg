// Package runtimeshim hosts the bounded set of //go:linkname accesses
// into Go runtime internals that goopg's hot paths rely on for
// RDBMS-grade performance.
//
// Callers MUST use only the exported API in this package; the
// linkname'd symbols themselves are unexported and confined here so
// that a Go-runtime symbol-shape change touches exactly one package.
//
// Each linkname site is paired with a tag-inverse fallback that uses
// public Go APIs. The fallback is correct but slower; it kicks in on
// any Go minor outside the explicitly-tested window
// (currently go1.24 .. go1.26).
//
// Discipline (per docs/design/perf-optimize/08-runtime-internals.md):
//
//   - One package holds every linkname site.
//   - One build-tag pattern: `go1.X && !go1.Y` where Y is the first
//     untested major. Bumping Y is the explicit "we tested it" gesture.
//   - One fallback file per linkname site, selected by the inverse tag.
//   - Every site compiles cleanly under -race.
//   - No //go:nosplit on linkname sites.
package runtimeshim
