package initdb

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bkiCreateColumns returns, per catalog, the ordered (name, type) column list
// of its `create <rel> <oid> … ( col = type , … )` declaration in PostgreSQL
// 18.3's postgres.bki.
func bkiCreateColumns(t *testing.T) map[string][][2]string {
	t.Helper()
	path := filepath.Join("..", "..", "postgres", "src", "include", "catalog", "postgres.bki")
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("PG oracle postgres.bki not available: %v", err)
	}
	defer f.Close()
	out := map[string][][2]string{}
	var cur string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "create "):
			cur = strings.Fields(line)[1]
		case cur != "" && line == " )":
			cur = ""
		case cur != "" && strings.Contains(line, " = "):
			fl := strings.Fields(strings.TrimSuffix(strings.TrimSpace(line), ","))
			out[cur] = append(out[cur], [2]string{fl[0], fl[2]})
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// bkiTypeOID maps a postgres.bki column type to its pg_type OID
// (pg_type.dat). Unknown names fail the test so the table stays complete.
var bkiTypeOID = map[string]uint32{
	"bool": 16, "bytea": 17, "char": 18, "name": 19, "int8": 20, "int2": 21,
	"int2vector": 22, "int4": 23, "regproc": 24, "text": 25, "oid": 26,
	"tid": 27, "xid": 28, "cid": 29, "oidvector": 30, "pg_node_tree": 194,
	"float4": 700, "float8": 701, "_int2": 1005, "_text": 1009, "_oid": 1028,
	"_char": 1002, "_float4": 1021, "_aclitem": 1034, "timestamptz": 1184,
	"anyarray": 2277, "pg_lsn": 3220, "regtype": 2206, "regclass": 2205,
	"_regtype": 2211, "pg_ndistinct": 3361, "pg_dependencies": 3402,
	"pg_mcv_list": 5017, "_pg_statistic": 10028, "xid8": 5069, "_float8": 1022,
	"_int8": 1016, "_int4": 1007, "_bool": 1000, "regnamespace": 4089,
	"regrole": 4096, "_name": 1003, "int4range": 3904, "jsonb": 3802,
}

// TestNailedRelAttrsMatchPostgresBKI pins every nailed catalog's attribute
// list (names, order, types) to its postgres.bki declaration (M0149-0001g
// slice 9). pg_operator was declared with 10 of its 15 columns, so a
// PostgreSQL node on a goopg directory answered `column "oprcode" does not
// exist` — psql \do and pg_dump's operator queries fail — although operators
// still executed (the syscache reads the physical tuple through the compiled
// FormData struct).
func TestNailedRelAttrsMatchPostgresBKI(t *testing.T) {
	bki := bkiCreateColumns(t)
	for _, rels := range [][]nailedRel{nailedSharedRels, nailedLocalRels} {
		for _, r := range rels {
			if r.RelKind != 'r' {
				continue
			}
			want, ok := bki[r.RelName]
			if !ok {
				continue // not a bki catalog (information_schema data tables, …)
			}
			checkNailedAttrsAgainstBKI(t, r.RelName+" (nailed descriptor)", r.Attrs, want)
			checkNailedAttrsAgainstBKI(t, r.RelName+" (pg_attribute heap rows)", pgAttrEntriesForRel(r), want)
			if int(r.RelNatts) != len(want) {
				t.Errorf("%s (%d): relnatts %d, want %d", r.RelName, r.OID, r.RelNatts, len(want))
			}
		}
	}
}

func checkNailedAttrsAgainstBKI(t *testing.T, label string, attrs []nailedAttr, want [][2]string) {
	t.Helper()
	{
		{
			r := struct{ RelName string }{label}
			var got []string
			for _, a := range attrs {
				if a.Num > 0 && !a.IsDropped {
					got = append(got, a.Name)
				}
			}
			var wantNames []string
			for i, c := range want {
				wantNames = append(wantNames, c[0])
				oid, known := bkiTypeOID[c[1]]
				if !known {
					t.Errorf("%s.%s: bki type %q missing from bkiTypeOID", r.RelName, c[0], c[1])
					continue
				}
				if i < len(attrs) && attrs[i].Name == c[0] && attrs[i].TypeOID != oid {
					t.Errorf("%s.%s: atttypid %d, want %d (%s)", r.RelName, c[0], attrs[i].TypeOID, oid, c[1])
				}
			}
			if strings.Join(got, ",") != strings.Join(wantNames, ",") {
				t.Errorf("%s: columns\n got  %v\n want %v", r.RelName, got, wantNames)
			}
		}
	}
}
