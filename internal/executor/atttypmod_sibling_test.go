package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/nodes"
)

// TestAttTypmodSiblingsAgree: pg_attribute.atttypmod (pgAttTypmod, keyed by
// type OID) and nodes.ColumnTypmod (keyed by type name; the pgoutput Relation
// message's per-column atttypmod uses it) port the same typmodin rules and
// must agree, or a subscriber sees a different type modifier than the
// publisher's catalog reports (Hard-won Rule 2).
func TestAttTypmodSiblingsAgree(t *testing.T) {
	cases := []struct {
		name string
		oid  uint32
		args []int64
	}{
		{"int4", 23, nil},
		{"text", 25, nil},
		{"numeric", 1700, nil},
		{"numeric", 1700, []int64{8}},
		{"numeric", 1700, []int64{8, 2}},
		{"varchar", 1043, []int64{10}},
		{"varchar", 1043, nil},
		{"bpchar", 1042, []int64{3}},
		{"bit", 1560, []int64{5}},
		{"varbit", 1562, []int64{9}},
		{"time", 1083, []int64{3}},
		{"timetz", 1266, []int64{2}},
		{"timestamp", 1114, []int64{0}},
		{"timestamptz", 1184, []int64{6}},
	}
	for _, c := range cases {
		byOID := pgAttTypmod(c.oid, c.args)
		byName := int64(nodes.ColumnTypmod(c.name, c.args))
		if byOID != byName {
			t.Errorf("%s%v: pgAttTypmod=%d, nodes.ColumnTypmod=%d", c.name, c.args, byOID, byName)
		}
	}
}
