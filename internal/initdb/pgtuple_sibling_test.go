package initdb

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 1 sibling-agreement gate.
//
// goopg already emits real index_form_tuple output — but only for the handful
// of fixed catalog shapes hand-rolled in btree_index_bootstrap.go, each with
// its tuple size worked out by hand in a comment. Those encoders are the
// project's strongest available oracle for the format: a real PostgreSQL 18.3
// reads the bootstrap catalog indexes they write, so any disagreement between
// them and the new general codec (internal/access/btree/pgtuple.go) is a bug
// in the new codec.
//
// This is the Hard-won-Rule-#2 twin test for S11.4: the general codec and the
// hand-rolled encoders must agree byte-for-byte before slices 2/3 start
// replacing writers with the general one. It is also what will catch a wrong
// alignment or a wrong hoff the moment either side is edited.
func TestPGIndexTupleMatchesBootstrapEncoders(t *testing.T) {
	const (
		heapBlk = 3
		heapOff = 5
	)
	tid := storage.ItemPointer{Block: heapBlk, Offset: heapOff}

	oid := btree.PGIndexAttr{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'}
	int2 := btree.PGIndexAttr{Len: 2, ByVal: true, AlignBy: 2, Storage: 'p'}
	// NameData is a fixed 64-byte struct with attalign 'c'.
	name := btree.PGIndexAttr{Len: 64, ByVal: false, AlignBy: 1, Storage: 'p'}

	nameDatum := func(s string) []byte {
		b := make([]byte, 64)
		copy(b, s)
		return b
	}

	cases := []struct {
		what  string
		attrs []btree.PGIndexAttr
		vals  [][]byte
		want  []byte
	}{
		{
			"oid (pg_class_oid_index)",
			[]btree.PGIndexAttr{oid},
			[][]byte{u32le(1259)},
			pgBuildIndexTupleOidKey(heapBlk, heapOff, 1259),
		},
		{
			"(oid,oid)",
			[]btree.PGIndexAttr{oid, oid},
			[][]byte{u32le(1259), u32le(11)},
			pgBuildIndexTupleOidOidKey(heapBlk, heapOff, 1259, 11),
		},
		{
			"(oid,int2) (pg_attribute_relid_attnum_index)",
			[]btree.PGIndexAttr{oid, int2},
			// attnum -2 (ctid) in two's complement.
			[][]byte{u32le(1259), u16le(0xFFFE)},
			pgBuildIndexTupleOidInt2Key(heapBlk, heapOff, 1259, -2),
		},
		{
			"name (pg_authid_rolname_index)",
			[]btree.PGIndexAttr{name},
			[][]byte{nameDatum("postgres")},
			pgBuildIndexTupleNameKey(heapBlk, heapOff, "postgres"),
		},
		{
			"(name,oid) (pg_class_relname_nsp_index)",
			[]btree.PGIndexAttr{name, oid},
			[][]byte{nameDatum("pg_class"), u32le(11)},
			pgBuildIndexTupleNameOidKey(heapBlk, heapOff, "pg_class", 11),
		},
		{
			"(oid,name)",
			[]btree.PGIndexAttr{oid, name},
			[][]byte{u32le(11), nameDatum("pg_class")},
			pgBuildIndexTupleOidNameKey(heapBlk, heapOff, 11, "pg_class"),
		},
		{
			"(name,oid,oid,oid) (pg_proc_proname_args_nsp_index shape)",
			[]btree.PGIndexAttr{name, oid, oid, oid},
			[][]byte{nameDatum("now"), u32le(1), u32le(2), u32le(3)},
			pgBuildIndexTupleNameOidOidOidKey(heapBlk, heapOff, "now", 1, 2, 3),
		},
		{
			"(oid,oid,oid,int2) (pg_amop shape)",
			[]btree.PGIndexAttr{oid, oid, oid, int2},
			[][]byte{u32le(1), u32le(23), u32le(23), u16le(1)},
			pgBuildIndexTupleOidOidOidInt2Key(heapBlk, heapOff, 1, 23, 23, 1),
		},
	}

	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			isnull := make([]bool, len(tc.attrs))
			got, err := btree.FormPGIndexTuple(tc.attrs, tc.vals, isnull, tid)
			if err != nil {
				t.Fatalf("FormPGIndexTuple: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("general codec disagrees with the PG-validated bootstrap encoder\n"+
					" got (%d bytes) %v\nwant (%d bytes) %v", len(got), got, len(tc.want), tc.want)
			}
			// The hand-rolled encoders record the size in t_info; the codec
			// must have derived the same one.
			if btree.PGIndexTupleSize(got) != len(tc.want) {
				t.Errorf("t_info size = %d, want %d", btree.PGIndexTupleSize(got), len(tc.want))
			}
			if btree.PGIndexTupleTID(got) != tid {
				t.Errorf("t_tid = %v, want %v", btree.PGIndexTupleTID(got), tid)
			}
			// Round-trip: every stored datum must come back byte-identical.
			vals, nulls, err := btree.DeformPGIndexTuple(got, tc.attrs, len(tc.attrs))
			if err != nil {
				t.Fatalf("DeformPGIndexTuple: %v", err)
			}
			for i := range tc.attrs {
				if nulls[i] {
					t.Errorf("attribute %d came back null", i)
					continue
				}
				if !bytes.Equal(vals[i], tc.vals[i]) {
					t.Errorf("attribute %d round-tripped as %v, want %v", i, vals[i], tc.vals[i])
				}
			}
		})
	}
}

func u16le(v uint16) []byte { return []byte{byte(v), byte(v >> 8)} }

func u32le(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}
