package executor

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
)

// M0130-S11.4 slice 3b-2b guards. The mapper's whole value is that it REFUSES
// to describe an index it cannot order exactly like PostgreSQL: a descriptor
// with a nil comparator would fall back to bytewise ordering, which silently
// corrupts the tree once the writer stores real datums. So the error cases
// below are as load-bearing as the happy path.

func keyDescTable(cols ...catalog.Column) *catalog.Table {
	t := &catalog.Table{Name: "t", Schema: "public"}
	for i := range cols {
		cols[i].Ordinal = i
	}
	t.Columns = cols
	return t
}

func col(name, typ string, args ...int64) catalog.Column {
	return catalog.Column{Name: name, Type: catalog.Type{Name: typ, Args: args}}
}

func TestBuildPGIndexKeyDescPhysicalLayout(t *testing.T) {
	tbl := keyDescTable(
		col("a", "int4"),
		col("b", "text"),
		col("c", "numeric"),
		col("d", "timestamptz"),
	)
	idx := &catalog.Index{Name: "i", Table: tbl, Method: "btree", Columns: []string{"a", "b", "c", "d"}}
	desc, err := buildPGIndexKeyDesc(idx)
	if err != nil {
		t.Fatalf("buildPGIndexKeyDesc: %v", err)
	}
	if desc.NKeyAtts() != 4 {
		t.Fatalf("NKeyAtts = %d, want 4", desc.NKeyAtts())
	}
	want := []btree.PGIndexAttr{
		{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'},   // int4
		{Len: -1, ByVal: false, AlignBy: 4, Storage: 'x'}, // text
		{Len: -1, ByVal: false, AlignBy: 4, Storage: 'm'}, // numeric
		{Len: 8, ByVal: true, AlignBy: 8, Storage: 'p'},   // timestamptz
	}
	for i, w := range want {
		if got := desc.Attrs[i].PGIndexAttr; got != w {
			t.Errorf("attr %d layout = %+v, want %+v", i, got, w)
		}
		if desc.Attrs[i].Compare == nil {
			t.Errorf("attr %d has nil Compare — a nil comparator means bytewise, which the mapper must never emit", i)
		}
	}
	// Physical() must project the same layouts the codec will consult.
	phys := desc.Physical()
	if len(phys) != 4 {
		t.Fatalf("Physical() len = %d, want 4", len(phys))
	}
	for i, w := range want {
		if phys[i] != w {
			t.Errorf("Physical()[%d] = %+v, want %+v", i, phys[i], w)
		}
	}
}

func TestBuildPGIndexKeyDescComparatorIsTheTypesOwn(t *testing.T) {
	tbl := keyDescTable(col("a", "int4"), col("b", "float8"), col("c", "oid"))
	idx := &catalog.Index{Name: "i", Table: tbl, Columns: []string{"a", "b", "c"}}
	desc, err := buildPGIndexKeyDesc(idx)
	if err != nil {
		t.Fatalf("buildPGIndexKeyDesc: %v", err)
	}
	i4 := func(v int32) []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(v))
		return b
	}
	// Signed int4: -1 < 1. A bytewise comparator would say the opposite, so
	// this also proves the mapper did not leave the default in place.
	if got := desc.Attrs[0].Compare(i4(-1), i4(1)); got >= 0 {
		t.Errorf("int4 compare(-1, 1) = %d, want < 0 (bytewise would be > 0)", got)
	}
	f8 := func(v float64) []byte {
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, math.Float64bits(v))
		return b
	}
	if got := desc.Attrs[1].Compare(f8(math.NaN()), f8(1e300)); got <= 0 {
		t.Errorf("float8 compare(NaN, 1e300) = %d, want > 0 (NaN is largest)", got)
	}
	// oid is UNSIGNED: 4000000000 > 1, where the int4 comparator would
	// disagree. Catches a mis-wired case arm in pgIndexComparatorForOID.
	u4 := func(v uint32) []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, v)
		return b
	}
	if got := desc.Attrs[2].Compare(u4(4000000000), u4(1)); got <= 0 {
		t.Errorf("oid compare(4000000000, 1) = %d, want > 0 (unsigned)", got)
	}
}

func TestBuildPGIndexKeyDescOrderingBits(t *testing.T) {
	tbl := keyDescTable(col("a", "int4"), col("b", "int4"), col("c", "int4"))
	// `... DESC NULLS LAST` is legal, so the two bits must be carried across
	// independently rather than one derived from the other.
	idx := &catalog.Index{
		Name: "i", Table: tbl, Columns: []string{"a", "b", "c"},
		ColDescending: []bool{false, true, true},
		ColNullsFirst: []bool{true, true, false},
	}
	desc, err := buildPGIndexKeyDesc(idx)
	if err != nil {
		t.Fatalf("buildPGIndexKeyDesc: %v", err)
	}
	for i, want := range []struct{ desc, nullsFirst bool }{
		{false, true}, {true, true}, {true, false},
	} {
		if desc.Attrs[i].Desc != want.desc || desc.Attrs[i].NullsFirst != want.nullsFirst {
			t.Errorf("attr %d = (desc=%v, nullsFirst=%v), want (%v, %v)",
				i, desc.Attrs[i].Desc, desc.Attrs[i].NullsFirst, want.desc, want.nullsFirst)
		}
	}
}

// The indoption slices are EMPTY when every key column is the default ASC
// NULLS LAST, and nothing guarantees a partially-filled slice is as long as
// Columns. An unchecked read is an index-out-of-range panic on the write path.
func TestBuildPGIndexKeyDescOrderingSlicesAreBoundsChecked(t *testing.T) {
	tbl := keyDescTable(col("a", "int4"), col("b", "int4"), col("c", "int4"))
	for _, tc := range []struct {
		name          string
		desc, nullsFF []bool
	}{
		{"empty", nil, nil},
		{"short", []bool{true}, []bool{true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := &catalog.Index{
				Name: "i", Table: tbl, Columns: []string{"a", "b", "c"},
				ColDescending: tc.desc, ColNullsFirst: tc.nullsFF,
			}
			d, err := buildPGIndexKeyDesc(idx)
			if err != nil {
				t.Fatalf("buildPGIndexKeyDesc: %v", err)
			}
			// Beyond the slice's end the answer is the PG default: ASC NULLS LAST.
			if d.Attrs[2].Desc || d.Attrs[2].NullsFirst {
				t.Errorf("attr 2 = (desc=%v, nullsFirst=%v), want the ASC NULLS LAST default",
					d.Attrs[2].Desc, d.Attrs[2].NullsFirst)
			}
		})
	}
}

// The unquoted `char` the parser gives a length arg is bpchar (varlena,
// blank-padded compare); the argument-less quoted `"char"` is the 1-byte
// internal type. Folding them would both mis-size the attribute and pick the
// wrong comparator.
func TestBuildPGIndexKeyDescCharVsBpchar(t *testing.T) {
	tbl := keyDescTable(col("q", "char"), col("p", "char", 10))
	idx := &catalog.Index{Name: "i", Table: tbl, Columns: []string{"q", "p"}}
	desc, err := buildPGIndexKeyDesc(idx)
	if err != nil {
		t.Fatalf("buildPGIndexKeyDesc: %v", err)
	}
	if desc.Attrs[0].Len != 1 || !desc.Attrs[0].ByVal {
		t.Errorf(`"char" attr = %+v, want the 1-byte by-value internal char`, desc.Attrs[0].PGIndexAttr)
	}
	if desc.Attrs[1].Len != -1 || desc.Attrs[1].ByVal {
		t.Errorf("char(10) attr = %+v, want a varlena bpchar", desc.Attrs[1].PGIndexAttr)
	}
	// bpchar ignores trailing blanks; the 1-byte char comparator does not.
	if got := desc.Attrs[1].Compare(bpcharVarlena("ab"), bpcharVarlena("ab  ")); got != 0 {
		t.Errorf("bpchar compare('ab', 'ab  ') = %d, want 0 (blank-padded)", got)
	}
}

// bpcharVarlena wraps a string in the 1-byte short varlena header the
// comparators expect from DeformPGIndexTuple.
func bpcharVarlena(s string) []byte {
	out := make([]byte, 0, len(s)+1)
	out = append(out, byte((len(s)+1)<<1|0x01))
	return append(out, s...)
}

func TestBuildPGIndexKeyDescRejects(t *testing.T) {
	base := keyDescTable(col("a", "int4"), col("s", "text"))
	enumTbl := keyDescTable(catalog.Column{Name: "m", Type: catalog.Type{Name: "mood"}})
	arrTbl := keyDescTable(catalog.Column{Name: "t", Type: catalog.Type{Name: "int4", IsArray: true}})

	for _, tc := range []struct {
		name string
		idx  *catalog.Index
		want string
	}{
		{"nil index", nil, "nil index"},
		{"no key columns", &catalog.Index{Name: "i", Table: base}, "no key columns"},
		{"no table", &catalog.Index{Name: "i", Columns: []string{"a"}}, "no table"},
		{"non-btree method", &catalog.Index{Name: "i", Table: base, Method: "gist", Columns: []string{"a"}}, "not btree"},
		{"expression key", &catalog.Index{Name: "i", Table: base, Columns: []string{""}}, "is an expression"},
		{"unknown column", &catalog.Index{Name: "i", Table: base, Columns: []string{"nope"}}, "not found"},
		{
			"explicit opclass",
			&catalog.Index{Name: "i", Table: base, Columns: []string{"s"}, ColOpClasses: []string{"text_pattern_ops"}},
			"operator class",
		},
		{
			"non-C collation",
			&catalog.Index{Name: "i", Table: base, Columns: []string{"s"}, ColCollations: []string{"en_US"}},
			"does not order bytewise",
		},
		// An enum column's catalog type name is unknown to the built-in table.
		// buildUserPGAttributeRow's resolution would fall back to `text` and
		// compare enum LABELS; goopg orders enums by sort order, so the mapper
		// must refuse instead.
		{"enum key", &catalog.Index{Name: "i", Table: enumTbl, Columns: []string{"m"}}, "no btree comparator"},
		{"array key", &catalog.Index{Name: "i", Table: arrTbl, Columns: []string{"t"}}, "no btree comparator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := buildPGIndexKeyDesc(tc.idx)
			if err == nil {
				t.Fatalf("buildPGIndexKeyDesc returned a descriptor (%d attrs), want an error", d.NKeyAtts())
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Every type with a 3b-2a comparator must be reachable through the catalog
// spellings a CREATE INDEX can use — a missing case arm silently downgrades a
// perfectly indexable column to "unsupported".
func TestBuildPGIndexKeyDescAcceptsEverySupportedSpelling(t *testing.T) {
	spellings := []string{
		"bool", "boolean", "bytea", "name",
		"int2", "smallint", "smallserial", "int4", "int", "integer", "serial",
		"int8", "bigint", "bigserial", "oid",
		"float4", "real", "float8", "double precision",
		"text", "varchar", "character varying", "bpchar",
		"numeric", "decimal", "uuid",
		"date", "time", "timetz", "timestamp", "timestamptz",
	}
	for _, sp := range spellings {
		t.Run(sp, func(t *testing.T) {
			tbl := keyDescTable(col("a", sp))
			d, err := buildPGIndexKeyDesc(&catalog.Index{Name: "i", Table: tbl, Columns: []string{"a"}})
			if err != nil {
				t.Fatalf("type %q: %v", sp, err)
			}
			if d.Attrs[0].Compare == nil {
				t.Fatalf("type %q: nil comparator", sp)
			}
			if d.Attrs[0].AlignBy == 0 {
				t.Fatalf("type %q: AlignBy 0 — attalign was never decoded", sp)
			}
		})
	}
}

// A domain column stores and orders exactly as its base type; the catalog has
// already rewritten Type.Name to the base, so no special case is needed — but
// a regression that started keying on DeclaredTypeName would break it.
func TestBuildPGIndexKeyDescDomainFollowsBaseType(t *testing.T) {
	tbl := keyDescTable(catalog.Column{
		Name: "d", Type: catalog.Type{Name: "int8"}, DeclaredTypeName: "positive_bigint",
	})
	d, err := buildPGIndexKeyDesc(&catalog.Index{Name: "i", Table: tbl, Columns: []string{"d"}})
	if err != nil {
		t.Fatalf("buildPGIndexKeyDesc: %v", err)
	}
	if d.Attrs[0].Len != 8 || !d.Attrs[0].ByVal || d.Attrs[0].AlignBy != 8 {
		t.Errorf("domain attr = %+v, want the int8 base layout", d.Attrs[0].PGIndexAttr)
	}
}
