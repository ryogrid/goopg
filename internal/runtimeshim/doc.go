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
//   - One build-tag pattern: `go1.X && !go1.Y && !noLinkname` where Y
//     is the first untested major. Bumping Y is the explicit "we tested
//     it" gesture; the `noLinkname` tag is an operator escape hatch
//     that forces the fallback path on a supported toolchain without
//     editing the tags. Use `go test -tags noLinkname ./...` to smoke
//     the fallback build on every loop that touches the package, per
//     chapter §10's verification gate.
//   - One fallback file per linkname site, selected by the inverse tag
//     (or by `noLinkname` on a supported toolchain).
//   - Every site compiles cleanly under -race.
//   - No //go:nosplit on linkname sites.
package runtimeshim
