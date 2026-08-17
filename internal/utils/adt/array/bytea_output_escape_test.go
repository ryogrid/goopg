package array

import "testing"

// TestByteaOutEscapeBoundaryBytes pins ByteaOutEscape against the traditional
// `bytea_output = escape` format PG 18.3 renders — a direct port of
// postgres/src/backend/utils/adt/varlena.c:397 byteaout's escape branch.
// Expectations captured LIVE from a PG 18.3 instance
// (postgres/local_install/bin), not transcribed by hand:
//
//	initdb -D /tmp/pgoracle-bytea-s12 -U postgres --no-sync
//	pg_ctl -D /tmp/pgoracle-bytea-s12 -o "-p 55391 -k /tmp" start
//	psql -h /tmp -p 55391 -U postgres -At
//	  set bytea_output = 'escape';
//	  select E'\\x00'::bytea;   -- \000
//	  select E'\\x1f'::bytea;   -- \037
//	  select E'\\x20'::bytea;   --  (a literal space)
//	  select E'\\x7e'::bytea;   -- ~
//	  select E'\\x7f'::bytea;   -- \177
//	  select E'\\x80'::bytea;   -- \200
//	  select E'\\xff'::bytea;   -- \377
//	  select E'\\x5c'::bytea;   -- \\
//	  select E'\\x'::bytea;     -- (empty)
//
// M0134-0001 S12.
func TestByteaOutEscapeBoundaryBytes(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"0x00 NUL", []byte{0x00}, `\000`},
		{"0x1f last-nonprintable-below-space", []byte{0x1f}, `\037`},
		{"0x20 space (printable boundary low)", []byte{0x20}, ` `},
		{"0x7e tilde (printable boundary high)", []byte{0x7e}, `~`},
		{"0x7f DEL", []byte{0x7f}, `\177`},
		{"0x80 high bit set", []byte{0x80}, `\200`},
		{"0xff", []byte{0xff}, `\377`},
		{"0x5c literal backslash", []byte{0x5c}, `\\`},
		{"empty", []byte{}, ``},
		{"mixed run", []byte{0x00, 0x01, 0x1f, 0x20, 0x7e, 0x7f, 0x80, 0xff, 0x5c},
			`\000\001\037 ~\177\200\377\\`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ByteaOutEscape(tc.in); got != tc.want {
				t.Errorf("ByteaOutEscape(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestByteaOutStyledDispatch pins the mode-dispatch helper every executor
// bytea-output site resolves through: "escape" (any case) selects
// ByteaOutEscape, everything else — "hex", "", an unrecognised spelling —
// falls back to ByteaOutHex, matching PG's SET-time enum validation (the
// output path never sees a value SET wouldn't have accepted). M0134-0001 S12.
func TestByteaOutStyledDispatch(t *testing.T) {
	b := []byte{0x00, 0x5c, 0xff}
	cases := []struct {
		mode string
		want string
	}{
		{"hex", ByteaOutHex(b)},
		{"", ByteaOutHex(b)},
		{"ESCAPE", ByteaOutEscape(b)},
		{"escape", ByteaOutEscape(b)},
		{"bogus", ByteaOutHex(b)},
	}
	for _, tc := range cases {
		if got := ByteaOutStyled(b, tc.mode); got != tc.want {
			t.Errorf("ByteaOutStyled(%v, %q) = %q, want %q", b, tc.mode, got, tc.want)
		}
	}
}
