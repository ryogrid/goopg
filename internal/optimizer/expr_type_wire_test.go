package optimizer

import (
	"testing"
)

// TestExprTypeWireTypeOID pins the planner exprType FuncCall arms whose sole
// job is to stamp the wire TypeOID: without them, ascii/crc32/crc32c/bit_count
// fall through to the default `catalog.Type{Name:"unknown"}` and the wire layer
// (typeOIDFor, internal/postmaster/dispatch.go) advertises TypeOID 25 (text),
// so psql's column_type_alignment (print.c:3614-3638) left-aligns these
// numeric columns instead of right-aligning them. The return types mirror
// pg_proc.dat (ascii -> int4 at :3610; crc32/crc32c -> int8 at :7954/:7957;
// bit_count -> int8 at :1534/:4201). M0134-0070.
func TestExprTypeWireTypeOID(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want string // type name as exprType reports it; int4 -> wire OID 23, int8 -> 20
	}{
		{"ascii('x')", "int4"},
		{"crc32('abc'::bytea)", "int8"},
		{"crc32c('abc'::bytea)", "int8"},
		{"bit_count('abc'::bytea)", "int8"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			got := exprType(resolveForTest(t, tc.sql))
			if got.Name != tc.want {
				t.Errorf("exprType(%q) = %q, want %q", tc.sql, got.Name, tc.want)
			}
		})
	}
}

// TestExprTypeWireTypeOIDNoOverreach guards against over-broad matching: the
// new cases must not change the type of an unrelated builtin, and an untouched
// integer-returning string function still reports int4 (not text/unknown).
func TestExprTypeWireTypeOIDNoOverreach(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want string
	}{
		{"to_hex(i)", "text"}, // unrelated builtin keeps its prior type
		{"length(s)", "int4"}, // prior int4 arm still intact
	} {
		t.Run(tc.sql, func(t *testing.T) {
			got := exprType(resolveForTest(t, tc.sql))
			if got.Name != tc.want {
				t.Errorf("exprType(%q) = %q, want %q", tc.sql, got.Name, tc.want)
			}
		})
	}
}
