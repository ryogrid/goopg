package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPgoPhysicalAlignMatchesPGTypalign is the review/260831-2 XL-5 guard.
// pgoutput carried its own hand-copied alignment table, a subset of the
// executor decoder's. It had drifted: pg_lsn, xid8, serial2/smallserial,
// serial8 and anyarray all fell through to the default 4 instead of their real
// pg_type typalign, so a replicated tuple containing one of those columns was
// read at the wrong offset — corrupting that column and every column after it
// on the wire. Both paths now share catalog.PhysicalTypeAlign; these are the
// typalign values from postgres/src/include/catalog/pg_type.dat.
func TestPgoPhysicalAlignMatchesPGTypalign(t *testing.T) {
	cases := []struct {
		typ  catalog.Type
		want int // pg_type typalign, in bytes
	}{
		{catalog.Type{Name: "pg_lsn"}, 8},      // 'd'
		{catalog.Type{Name: "xid8"}, 8},        // 'd'
		{catalog.Type{Name: "xid"}, 4},         // 'i'
		{catalog.Type{Name: "anyarray"}, 8},    // 'd'
		{catalog.Type{Name: "smallserial"}, 2}, // int2, 's'
		{catalog.Type{Name: "serial2"}, 2},     // int2, 's'
		{catalog.Type{Name: "serial8"}, 8},     // int8, 'd'
		{catalog.Type{Name: "int2"}, 2},
		{catalog.Type{Name: "int8"}, 8},
		{catalog.Type{Name: "uuid"}, 1}, // 'c'
		{catalog.Type{Name: "name"}, 1}, // 'c'
		{catalog.Type{Name: "int4", IsArray: true}, 4},
	}
	for _, c := range cases {
		// pgoPhysicalAlign rounds up, so an offset of 1 exposes the alignment.
		got := pgoPhysicalAlign(1, c.typ)
		want := 1
		if c.want > 1 {
			want = c.want
		}
		if got != want {
			t.Errorf("pgoPhysicalAlign(1, %s) = %d, want %d (typalign %d)", c.typ.Name, got, want, c.want)
		}
	}
}
