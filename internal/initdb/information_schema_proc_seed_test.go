// Manifest ↔ Go-table agreement guard for the information_schema helper
// functions (M0133-S2).
//
// scripts/capture-ev-action.sh --prosqlbody captures the 11 helper functions
// information_schema.sql creates from a throwaway real PG 18.3, writing
// internal/initdb/information_schema_proc_manifest.tsv (the full pg_proc row)
// plus a <name>_prosqlbody.dat blob per function with a non-null prosqlbody.
// cmd/gen-information-schema-procs renders the manifest into
// information_schema_proc_seed.go (informationSchemaHelperProcs), and the blob
// set is embedded via information_schema_proc_sqlbody.go.
//
// The two tests below re-check the generated Go table and the embedded blob set
// against the oracle's own capture, offline. Drift is either a skipped
// generator run or a hand-edit that didn't re-capture — the "forgot to
// regenerate" case nailed_view_manifest_test.go guards for the view corpus.
//
// The manifest is checked in, so this runs with no PG involved. Only
// re-capturing (scripts/capture-ev-action.sh --prosqlbody --verify) needs the
// oracle.

package initdb

import (
	"bufio"
	"os"
	"sort"
	"strings"
	"testing"
)

const informationSchemaProcManifestPath = "information_schema_proc_manifest.tsv"

type manifestProc struct {
	Name       string
	OID        uint32
	Namespace  uint32
	Cost       int64
	Rows       int64
	Support    uint32
	Kind       byte
	Volatile   byte
	Parallel   byte
	IsStrict   bool
	RetSet     bool
	Lang       uint32
	RetType    uint32
	ArgTypes   []uint32
	AllArgType []uint32
	ArgModes   []byte
	ArgNames   []string
	ProcSrc    string
	HasSqlBody bool
}

// parseProcManifest reads the TSV emitted by capture-ev-action.sh --prosqlbody.
func parseProcManifest(t *testing.T) []manifestProc {
	t.Helper()
	f, err := os.Open(informationSchemaProcManifestPath)
	if err != nil {
		t.Fatalf("open manifest: %v (regenerate with scripts/capture-ev-action.sh --prosqlbody)", err)
	}
	defer f.Close()

	var procs []manifestProc
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		if fields[0] != "proc" {
			t.Fatalf("manifest line %d: unknown record kind %q", line, fields[0])
		}
		if len(fields) != 20 {
			t.Fatalf("manifest line %d: proc row has %d fields, want 20: %q", line, len(fields), text)
		}
		procs = append(procs, manifestProc{
			Name:       fields[1],
			OID:        mustU32(t, line, fields[2]),
			Namespace:  mustU32(t, line, fields[3]),
			Cost:       mustI64(t, line, fields[4]),
			Rows:       mustI64(t, line, fields[5]),
			Support:    mustU32(t, line, fields[6]),
			Kind:       fields[7][0],
			Volatile:   fields[8][0],
			Parallel:   fields[9][0],
			IsStrict:   mustBool(t, line, fields[10]),
			RetSet:     mustBool(t, line, fields[11]),
			Lang:       mustU32(t, line, fields[12]),
			RetType:    mustU32(t, line, fields[13]),
			ArgTypes:   parseProcOidVector(t, line, fields[14]),
			ProcSrc:    fields[18],
			HasSqlBody: fields[19] != "-",
		})
		last := &procs[len(procs)-1]
		if fields[15] != "-" {
			for _, s := range parseProcArrayText(t, line, fields[15]) {
				last.AllArgType = append(last.AllArgType, mustU32(t, line, s))
			}
		}
		if fields[16] != "-" {
			for _, s := range parseProcArrayText(t, line, fields[16]) {
				if len(s) != 1 {
					t.Fatalf("manifest line %d: proargmodes element %q is not one char", line, s)
				}
				last.ArgModes = append(last.ArgModes, s[0])
			}
		}
		if fields[17] != "-" {
			last.ArgNames = parseProcArrayText(t, line, fields[17])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan manifest: %v", err)
	}
	if len(procs) == 0 {
		t.Fatal("manifest describes no procs")
	}
	return procs
}

func parseProcOidVector(t *testing.T, line int, s string) []uint32 {
	t.Helper()
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Fields(s)
	out := make([]uint32, len(parts))
	for i, p := range parts {
		out[i] = mustU32(t, line, p)
	}
	return out
}

// parseProcArrayText parses a PG array literal ::text, or "-" (NULL) → nil.
func parseProcArrayText(t *testing.T, line int, s string) []string {
	t.Helper()
	if s == "-" {
		return nil
	}
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		t.Fatalf("manifest line %d: %q is not a PG array literal", line, s)
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return []string{}
	}
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if inQuote {
			switch c {
			case '\\':
				i++
				if i < len(inner) {
					cur.WriteByte(inner[i])
				}
			case '"':
				inQuote = false
			default:
				cur.WriteByte(c)
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case ',':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	return out
}

// TestInformationSchemaHelperProcsMatchManifest re-checks the generated Go
// entries against the oracle capture, field by field.
func TestInformationSchemaHelperProcsMatchManifest(t *testing.T) {
	procs := parseProcManifest(t)
	entries := informationSchemaHelperProcs()

	// Non-vacuity: exactly the 11 measured helpers, and a 1:1 OID set.
	if got, want := len(entries), len(procs); got != want {
		t.Fatalf("informationSchemaHelperProcs has %d entries, manifest has %d — re-run scripts/capture-ev-action.sh --prosqlbody", got, want)
	}
	if len(procs) != 11 {
		t.Fatalf("manifest carries %d procs, want 11", len(procs))
	}

	byOID := map[uint32]pgProcEntry{}
	for _, e := range entries {
		byOID[e.OID] = e
	}

	for _, m := range procs {
		e, ok := byOID[m.OID]
		if !ok {
			t.Errorf("%s: OID %d in the manifest but not in informationSchemaHelperProcs()", m.Name, m.OID)
			continue
		}
		if e.Name != m.Name {
			t.Errorf("OID %d: Go name %q != manifest %q", m.OID, e.Name, m.Name)
		}
		if e.Namespace != m.Namespace {
			t.Errorf("%s: namespace %d != manifest %d", m.Name, e.Namespace, m.Namespace)
		}
		if e.Cost != m.Cost {
			t.Errorf("%s: cost %d != manifest %d", m.Name, e.Cost, m.Cost)
		}
		if e.Rows != m.Rows {
			t.Errorf("%s: rows %d != manifest %d", m.Name, e.Rows, m.Rows)
		}
		if e.Support != m.Support {
			t.Errorf("%s: support %d != manifest %d", m.Name, e.Support, m.Support)
		}
		if e.Volatile != m.Volatile {
			t.Errorf("%s: volatile %q != manifest %q", m.Name, e.Volatile, m.Volatile)
		}
		if e.Parallel != m.Parallel {
			t.Errorf("%s: parallel %q != manifest %q", m.Name, e.Parallel, m.Parallel)
		}
		if e.NotStrict != !m.IsStrict {
			t.Errorf("%s: NotStrict %v != !isstrict(%v)", m.Name, e.NotStrict, m.IsStrict)
		}
		if e.RetSet != m.RetSet {
			t.Errorf("%s: retset %v != manifest %v", m.Name, e.RetSet, m.RetSet)
		}
		if e.Lang != m.Lang {
			t.Errorf("%s: lang %d != manifest %d", m.Name, e.Lang, m.Lang)
		}
		if e.RetType != m.RetType {
			t.Errorf("%s: rettype %d != manifest %d", m.Name, e.RetType, m.RetType)
		}
		if !eqU32(e.ArgTypes, m.ArgTypes) {
			t.Errorf("%s: argtypes %v != manifest %v", m.Name, e.ArgTypes, m.ArgTypes)
		}
		if !eqU32(e.AllArgTypes, m.AllArgType) {
			t.Errorf("%s: allargtypes %v != manifest %v", m.Name, e.AllArgTypes, m.AllArgType)
		}
		if !eqBytes(e.ArgModes, m.ArgModes) {
			t.Errorf("%s: argmodes %v != manifest %v", m.Name, e.ArgModes, m.ArgModes)
		}
		if !eqStr(e.ArgNames, m.ArgNames) {
			t.Errorf("%s: argnames %v != manifest %v", m.Name, e.ArgNames, m.ArgNames)
		}
		if e.HandlerName != m.ProcSrc {
			t.Errorf("%s: prosrc %q != manifest %q", m.Name, e.HandlerName, m.ProcSrc)
		}
		if (e.SqlBody != "") != m.HasSqlBody {
			t.Errorf("%s: SqlBody set %v != manifest has-sqlbody %v", m.Name, e.SqlBody != "", m.HasSqlBody)
		}
		// A prosrc that says one thing and a prosqlbody that says another would
		// make PG's fmgr execute the wrong one. The 10 SQL-body helpers carry
		// prosrc='' + a SqlBody name; _pg_expandarray is the sole inverse.
		if m.HasSqlBody && e.HandlerName != "" {
			t.Errorf("%s: has a prosqlbody but also a non-empty prosrc %q (PG runs prosrc only when prosqlbody is NULL)", m.Name, e.HandlerName)
		}
	}
}

// TestInformationSchemaProcSqlBodyBlobsMatchManifest pins the embedded blob set
// against the manifest: every helper with a non-null prosqlbody must have a
// committed .dat blob, and every blob must belong to a manifest row.
func TestInformationSchemaProcSqlBodyBlobsMatchManifest(t *testing.T) {
	procs := parseProcManifest(t)

	want := map[string]int{} // blob name → declared length (0 = should not exist)
	manifestNames := []string{}
	for _, m := range procs {
		manifestNames = append(manifestNames, m.Name)
		if m.HasSqlBody {
			want[m.Name] = 1
		} else {
			want[m.Name] = 0
		}
	}

	got := nailedProcSqlBodyBlobs()
	sort.Strings(got)

	var expected []string
	for name, present := range want {
		if present == 1 {
			expected = append(expected, name)
		}
	}
	sort.Strings(expected)
	if !eqStr(got, expected) {
		t.Errorf("embedded blob set %v != manifest non-null prosqlbody set %v", got, expected)
	}

	// Every blob must round-trip through the runtime lookup (non-empty, single
	// line). This is the same panic-on-wrong-data contract the seed relies on.
	for _, name := range got {
		if s := nailedProcSqlBody(name); s == "" {
			t.Errorf("%s: empty prosqlbody blob", name)
		}
	}

	// The sole textual-prosrc helper must resolve nothing here.
	for _, name := range manifestNames {
		if want[name] == 0 {
			if _, err := procSqlBodyFS.ReadFile(nailedProcSqlBodyFile(name)); err == nil {
				t.Errorf("%s: has a prosqlbody blob but the manifest says prosqlbody is NULL", name)
			}
		}
	}
}

func eqU32(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func eqBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func eqStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
