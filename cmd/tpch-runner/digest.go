package main

// Result digests (M0127-P5.9-d).
//
// Why this exists: until 2026-08-05 the runner reported `rows=N` and nothing
// else, so the S5 acceptance bar's clause 1 ("22/22 complete, no row-count
// mismatch") compared two arms on cardinality alone. The P5.9 run 1 defect
// (docs/design/leftdeep-joins/09-verification-and-acceptance.md §3.1) returned
// the RIGHT number of rows with every column value shifted one relation-block
// from its name; five ON-arm queries "matched" without their tuples ever being
// compared, and the bar only noticed because three other queries happened to
// raise 42883. A bar that relies on a query being loud measures the query, not
// the engine.
//
// So the runner can now compute two digests per result set:
//
//   - ordered:   FNV-1a/64 chained over the rows in scan order. Sensitive to
//                values, to NULL-vs-empty, to column count, and to row order.
//   - unordered: the wrapping sum of the per-row hashes. Sum is commutative,
//                so this is a MULTISET digest — it is unchanged by row order
//                but (unlike XOR) still counts duplicates. This is the
//                authoritative equality check for a query whose ORDER BY is
//                not a total order, where two correct arms may legitimately
//                break ties differently.
//   - colsig:    FNV-1a/64 over the column NAMES in order, so a permuted or
//                renamed output header is reported separately from a value
//                difference rather than being folded into it.
//
// Encoding is length-prefixed, not separator-delimited: a text column may
// contain any byte, so a delimiter would be forgeable (two different rows
// hashing alike). NULL is a distinct one-byte marker, so NULL and '' differ.

import (
	"database/sql"
	"encoding/binary"
)

const (
	fnvOffset64 = uint64(14695981039346656037)
	fnvPrime64  = uint64(1099511628211)
)

// digestNullMarker / digestValueMarker keep NULL distinguishable from the
// empty string; without them `NULL` and `''` hash identically.
const (
	digestNullMarker  = byte(0x00)
	digestValueMarker = byte(0x01)
)

// fnvAdd folds b into the running FNV-1a/64 state h.
func fnvAdd(h uint64, b []byte) uint64 {
	for _, c := range b {
		h ^= uint64(c)
		h *= fnvPrime64
	}
	return h
}

// fnv1a64 hashes b standalone.
func fnv1a64(b []byte) uint64 { return fnvAdd(fnvOffset64, b) }

// resultDigest accumulates the digests of one result set. Zero value is not
// usable; construct with newResultDigest so `ordered` starts at the FNV offset
// basis (a zero seed would make the first row's contribution vanish).
type resultDigest struct {
	cols      []string
	colsig    uint64
	ordered   uint64
	unordered uint64 // wrapping sum of per-row hashes: order-independent
	rows      int

	buf []byte // reused row-encoding scratch
}

func newResultDigest(cols []string) *resultDigest {
	d := &resultDigest{cols: cols, ordered: fnvOffset64, colsig: fnvOffset64}
	for _, c := range cols {
		d.colsig = fnvAdd(d.colsig, binary.AppendUvarint(nil, uint64(len(c))))
		d.colsig = fnvAdd(d.colsig, []byte(c))
	}
	return d
}

// addRow folds one scanned row into both digests. vals holds one entry per
// column; a nil entry is SQL NULL. The bytes are only read here, so passing
// sql.RawBytes (valid only until the next Next()) is safe.
func (d *resultDigest) addRow(vals []sql.RawBytes) {
	d.buf = d.buf[:0]
	d.buf = binary.AppendUvarint(d.buf, uint64(len(vals)))
	for _, v := range vals {
		if v == nil {
			d.buf = append(d.buf, digestNullMarker)
			continue
		}
		d.buf = append(d.buf, digestValueMarker)
		d.buf = binary.AppendUvarint(d.buf, uint64(len(v)))
		d.buf = append(d.buf, v...)
	}
	d.ordered = fnvAdd(d.ordered, d.buf)
	d.unordered += fnv1a64(d.buf) // wrapping add — multiset digest
	d.rows++
}

// scanTargets returns the Scan destinations for a row: every column is taken
// as sql.RawBytes so the digest sees the driver's own byte rendering rather
// than a Go-typed round trip. database/sql converts int/float/bool/time.Time
// sources to RawBytes deterministically (time.Time via RFC3339Nano), and NULL
// arrives as a nil RawBytes.
func scanTargets(vals []sql.RawBytes) []any {
	out := make([]any, len(vals))
	for i := range vals {
		out[i] = &vals[i]
	}
	return out
}

// digestFields renders the three digest tokens appended to an OK line. Kept
// here so the runner's output format and the diff mode's parser (digestdiff.go)
// have exactly one definition of the token names between them.
func (d *resultDigest) fields() string {
	return "colsig=" + hex16(d.colsig) + " ordered=" + hex16(d.ordered) + " unordered=" + hex16(d.unordered)
}

// hex16 renders a uint64 as a fixed-width 16-digit lowercase hex string, so
// digests line up column-wise in a log a human has to scan.
func hex16(v uint64) string {
	const digits = "0123456789abcdef"
	var b [16]byte
	for i := 15; i >= 0; i-- {
		b[i] = digits[v&0xf]
		v >>= 4
	}
	return string(b[:])
}
