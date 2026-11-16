package catalog

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// knownAlignGaps are pg_type rows whose typalign PhysicalTypeAlign does not
// model, each for a stated reason. A new mismatch outside this list fails the
// test; fixing one means deleting its entry.
var knownAlignGaps = map[string]string{
	// Fixed-length PG types goopg stores as a varlena (PhysicalTypeIsVarlena
	// default) rather than PG's fixed-length form: alignment alone cannot make
	// them PG-readable, the storage format must change first.
	"tid": "scalar stored as varlena", "point": "scalar stored as varlena",
	"lseg": "scalar stored as varlena", "box": "scalar stored as varlena",
	"line": "scalar stored as varlena", "circle": "scalar stored as varlena",
	"money": "scalar stored as varlena", "aclitem": "scalar stored as varlena",
	// Composite rowtypes: typalign is the strictest member alignment; goopg
	// does not derive it per composite yet.
	"pg_type": "composite", "pg_attribute": "composite", "pg_proc": "composite", "pg_class": "composite",
	"pg_type[]": "composite", "pg_attribute[]": "composite", "pg_proc[]": "composite", "pg_class[]": "composite",
	// Pseudo-types never stored in a heap column.
	"unknown": "pseudo", "record": "pseudo", "record[]": "pseudo", "cstring": "pseudo",
	"internal": "pseudo", "pg_ddl_command": "pseudo", "anyrange": "pseudo",
	"anycompatiblearray": "pseudo", "anycompatiblerange": "pseudo",
	"anymultirange": "pseudo", "anycompatiblemultirange": "pseudo",
}

// TestPhysicalTypeAlignMatchesPostgresBKI pins PhysicalTypeAlign — the single
// alignment table shared by the heap codec and pgoutput — to every pg_type row
// of PostgreSQL 18.3's postgres.bki (M0149-0001g slice 14). Alignment drift has
// already broken hosted-PG reads twice (anyarray in M0131-S14.2, _aclitem in
// slice 13): a column padded to 4 where PG expects 8 leaves a zero pad byte that
// PG's att_align_pointer treats as padding, so it reads the value one word late.
// Array types are checked the way goopg names a user array column (element
// Name with IsArray set).
func TestPhysicalTypeAlignMatchesPostgresBKI(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "postgres", "src", "include", "catalog", "postgres.bki"))
	if err != nil {
		t.Skipf("PG oracle postgres.bki not available: %v", err)
	}
	defer f.Close()
	type row struct{ name, elem, align string }
	var rows []row
	nameByOID := map[string]string{}
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "create pg_type "):
			in = true
		case l == "close pg_type":
			in = false
		case in && strings.HasPrefix(l, "insert ( "):
			fl := strings.Fields(strings.TrimPrefix(l, "insert ( "))
			// oid typname … typelem (13) … typalign (22)
			rows = append(rows, row{fl[1], fl[13], fl[22]})
			nameByOID[fl[0]] = fl[1]
		}
	}
	if len(rows) < 100 {
		t.Fatalf("parsed only %d pg_type rows", len(rows))
	}
	bytes := map[string]int{"c": 1, "s": 2, "i": 4, "d": 8}
	for _, r := range rows {
		label, got := r.name, 0
		if r.elem != "0" && strings.HasPrefix(r.name, "_") {
			en := nameByOID[r.elem]
			label = en + "[]"
			got = PhysicalTypeAlign(Type{Name: en, IsArray: true}, en)
		} else {
			got = PhysicalTypeAlign(Type{Name: r.name}, r.name)
		}
		want, ok := bytes[r.align]
		if !ok { // ALIGNOF_POINTER placeholders (pseudo-types)
			continue
		}
		if _, gap := knownAlignGaps[label]; gap {
			if got == want {
				t.Errorf("%s now matches PG (%d); remove it from knownAlignGaps", label, want)
			}
			continue
		}
		if got != want {
			t.Errorf("%s: PhysicalTypeAlign = %d, pg_type.dat typalign %q = %d", label, got, r.align, want)
		}
	}
}
