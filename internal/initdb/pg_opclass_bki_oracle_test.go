package initdb

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bkiInserts returns the `insert ( … )` field lists of one catalog's section in
// PostgreSQL 18.3's postgres.bki (between `create <rel> <oid>` and
// `close <rel>`). The test is skipped when the oracle source is not checked out.
func bkiInserts(t *testing.T, rel string) [][]string {
	t.Helper()
	path := filepath.Join("..", "..", "postgres", "src", "include", "catalog", "postgres.bki")
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("PG oracle postgres.bki not available: %v", err)
	}
	defer f.Close()
	var out [][]string
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "create "+rel+" "):
			in = true
		case line == "close "+rel:
			in = false
		case in && strings.HasPrefix(line, "insert ( "):
			out = append(out, strings.Fields(strings.TrimSuffix(strings.TrimPrefix(line, "insert ( "), " )")))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatalf("no %s rows found in %s", rel, path)
	}
	return out
}

// TestPgOpclassMatchesPostgresBKI pins goopg's bootstrap pg_opclass rows to
// PostgreSQL 18.3's, row for row (M0149-0001g slice 7). goopg had used private
// OIDs (a 9000 series plus 1984-1987) for opclasses without an explicit OID in
// pg_opclass.dat, while pg_index.indclass and goopg's own runtime used PG's
// genbki OIDs, so a PostgreSQL node on a goopg directory failed "could not find
// tuple for opclass 10012" opening pg_enum's index. It also had the spgist and
// BRIN access-method OIDs swapped, so BRIN opened spgist support functions.
func TestPgOpclassMatchesPostgresBKI(t *testing.T) {
	want := map[string]string{}
	for _, f := range bkiInserts(t, "pg_opclass") {
		// oid opcmethod opcname opcnamespace opcowner opcfamily opcintype opcdefault opckeytype
		want[f[0]] = strings.Join(f, " ")
	}
	got := map[string]string{}
	for _, e := range pgOpclassInitialEntries() {
		def := "f"
		if e.Default {
			def = "t"
		}
		got[fmt.Sprint(e.OID)] = fmt.Sprintf("%d %d %s %d %d %d %d %s %d",
			e.OID, e.Method, e.Name, e.Namespace, e.Owner, e.Family, e.IntType, def, e.KeyType)
	}
	compareBKIRows(t, "pg_opclass", want, got)
}

// TestPgOpfamilyMatchesPostgresBKI is the pg_opfamily twin (oid opfmethod
// opfname opfnamespace opfowner).
func TestPgOpfamilyMatchesPostgresBKI(t *testing.T) {
	want := map[string]string{}
	for _, f := range bkiInserts(t, "pg_opfamily") {
		want[f[0]] = strings.Join(f, " ")
	}
	got := map[string]string{}
	for _, e := range pgOpfamilyInitialEntries() {
		got[fmt.Sprint(e.OID)] = fmt.Sprintf("%d %d %s %d %d", e.OID, e.Method, e.Name, e.Namespace, e.Owner)
	}
	compareBKIRows(t, "pg_opfamily", want, got)
}

func compareBKIRows(t *testing.T, rel string, want, got map[string]string) {
	t.Helper()
	for oid, w := range want {
		g, ok := got[oid]
		switch {
		case !ok:
			t.Errorf("%s: PG row missing from goopg: %s", rel, w)
		case g != w:
			t.Errorf("%s OID %s:\n got  %s\n want %s", rel, oid, g, w)
		}
	}
	for oid, g := range got {
		if _, ok := want[oid]; !ok {
			t.Errorf("%s: goopg row not in PG 18.3: %s", rel, g)
		}
	}
}
