package executor

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestCreateIndexRejectsMutableExpressions is the revert-check guard for
// M0134-0170: every statement below was ACCEPTED by goopg before the change
// and is rejected by PG 18.3 (verified live via scripts/pg-oracle-diff.sh
// --auto-start). No upstream regress case in goopg's ported set exercises
// these errors outside sqljson_queryfuncs.sql, whose surrounding statements
// need the SQL/JSON subsystem, so this test is the only coverage.
func TestCreateIndexRejectsMutableExpressions(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		`CREATE TABLE zz_mut (a int, b text, c timestamp)`,
		// A LANGUAGE sql routine is INLINED before PG's volatility test, so its
		// declared marker is irrelevant and only the body counts — verified
		// against the oracle: PG ACCEPTS an index on a VOLATILE sql function
		// whose body is `SELECT $1`, and rejects the same body written in
		// plpgsql. Hence one of each here.
		`CREATE FUNCTION zz_mut_vol(int) RETURNS int VOLATILE LANGUAGE plpgsql AS $$ BEGIN RETURN $1; END $$`,
		`CREATE FUNCTION zz_mut_stb(int) RETURNS int STABLE LANGUAGE plpgsql AS $$ BEGIN RETURN $1; END $$`,
		`CREATE FUNCTION zz_mut_imm(int) RETURNS int IMMUTABLE LANGUAGE plpgsql AS $$ BEGIN RETURN $1; END $$`,
		`CREATE FUNCTION zz_mut_sqlvol() RETURNS int VOLATILE LANGUAGE sql AS 'SELECT (random()*10)::int'`,
		`CREATE FUNCTION zz_mut_sqlok(int) RETURNS int VOLATILE LANGUAGE sql AS 'SELECT $1'`,
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("setup %q: %v", ddl, err)
		}
	}

	rejected := []struct {
		name string
		ddl  string
		want string
	}{
		{"volatile builtin in expression",
			`CREATE INDEX zz_i1 ON zz_mut ((a + (random()*10)::int))`,
			"functions in index expression must be marked IMMUTABLE"},
		{"stable builtin in expression",
			`CREATE INDEX zz_i2 ON zz_mut ((now()::text))`,
			"functions in index expression must be marked IMMUTABLE"},
		{"volatile builtin with no column reference",
			`CREATE INDEX zz_i3 ON zz_mut ((clock_timestamp()::text))`,
			"functions in index expression must be marked IMMUTABLE"},
		{"user VOLATILE plpgsql function",
			`CREATE INDEX zz_i4 ON zz_mut (zz_mut_vol(a))`,
			"functions in index expression must be marked IMMUTABLE"},
		{"user STABLE plpgsql function",
			`CREATE INDEX zz_i5 ON zz_mut (zz_mut_stb(a))`,
			"functions in index expression must be marked IMMUTABLE"},
		// The walker must reach past every container node, or the gate is
		// bypassable by wrapping the call in one of them.
		{"volatile inside a CASE arm",
			`CREATE INDEX zz_i6 ON zz_mut ((CASE WHEN a > 0 THEN timeofday() ELSE '' END))`,
			"functions in index expression must be marked IMMUTABLE"},
		{"volatile inside an ARRAY constructor",
			`CREATE INDEX zz_i7 ON zz_mut ((ARRAY[a, (random()*10)::int]))`,
			"functions in index expression must be marked IMMUTABLE"},
		// A LANGUAGE sql routine IS inlined, so a volatile BODY still rejects.
		{"sql routine whose inlined body is volatile",
			`CREATE INDEX zz_i10 ON zz_mut ((a + zz_mut_sqlvol()))`,
			"functions in index expression must be marked IMMUTABLE"},
		{"volatile in a partial-index predicate",
			`CREATE INDEX zz_i8 ON zz_mut (a) WHERE a > (random()*10)::int`,
			"functions in index predicate must be marked IMMUTABLE"},
		{"stable user function in a partial-index predicate",
			`CREATE INDEX zz_i9 ON zz_mut (a) WHERE zz_mut_stb(a) > 1`,
			"functions in index predicate must be marked IMMUTABLE"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			err := runDDL(t, ctx, tc.ddl)
			if err == nil {
				t.Fatalf("%s: accepted, want %q", tc.ddl, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: got %v, want %q", tc.ddl, err, tc.want)
			}
			ee, ok := err.(*ExecError)
			if !ok || ee.Code != "42P17" {
				t.Fatalf("%s: got SQLSTATE %v, want 42P17 (ERRCODE_INVALID_OBJECT_DEFINITION)", tc.ddl, err)
			}
		})
	}

	// The gate must not fire on anything PG accepts (each verified live against
	// PG 18.3). `date_trunc('day', <timestamp>)` is the false-positive canary:
	// date_trunc has BOTH an immutable (timestamp) and a stable (timestamptz)
	// overload upstream, so it is deliberately absent from
	// nonImmutableBuiltinNames — listing it would reject this legal index.
	// zz_mut_sqlok is the inlining canary: declared VOLATILE, but PG inlines
	// `SELECT $1` and accepts.
	for _, ddl := range []string{
		`CREATE INDEX zz_ok1 ON zz_mut ((a * 2))`,
		`CREATE INDEX zz_ok2 ON zz_mut (lower(b))`,
		`CREATE INDEX zz_ok3 ON zz_mut (zz_mut_imm(a))`,
		`CREATE INDEX zz_ok4 ON zz_mut (a) WHERE zz_mut_imm(a) > 1`,
		`CREATE INDEX zz_ok5 ON zz_mut ((date_trunc('day', c)))`,
		`CREATE INDEX zz_ok6 ON zz_mut (zz_mut_sqlok(a))`,
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Errorf("%s: rejected, want accepted: %v", ddl, err)
		}
	}
}

// TestPartitionKeyRejectsMutableBuiltin covers the sibling half of the same
// upstream predicate (ComputePartitionAttrs). Before M0134-0170 the partition
// path consulted only user-defined routines, so a bare volatile BUILT-IN in a
// partition key expression was accepted.
func TestPartitionKeyRejectsMutableBuiltin(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, `CREATE TABLE zz_pk (a int) PARTITION BY RANGE ((a + (random()*10)::int))`)
	if err == nil {
		t.Fatalf("accepted, want 42P16 functions in partition key expression must be marked IMMUTABLE")
	}
	if !strings.Contains(err.Error(), "functions in partition key expression must be marked IMMUTABLE") {
		t.Fatalf("got %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE zz_pk_ok (a int, b text) PARTITION BY RANGE (lower(b))`); err != nil {
		t.Errorf("lower(b) partition key rejected, want accepted: %v", err)
	}
}

// TestNonImmutableBuiltinsMatchPgProcDat re-derives the table in
// pg_nonimmutable_builtins.go straight from the in-tree PG 18.3 oracle, so it
// cannot silently drift from upstream: a name is listed iff NO pg_proc.dat
// entry for that name declares provolatile 'i' (BKI_DEFAULT(i) means an
// omitted field is immutable). Skips when the ./postgres convenience symlink
// is absent — it is untracked.
func TestNonImmutableBuiltinsMatchPgProcDat(t *testing.T) {
	dat := filepath.Join("..", "..", "postgres", "src", "include", "catalog", "pg_proc.dat")
	raw, err := os.ReadFile(dat)
	if err != nil {
		t.Skipf("PG oracle not available (%v)", err)
	}
	entry := regexp.MustCompile(`(?s)\{ oid =.*?\}\s*,\s*\n`)
	proname := regexp.MustCompile(`proname => '([^']*)'`)
	provolatile := regexp.MustCompile(`provolatile => '([^']*)'`)

	vols := map[string]map[string]bool{}
	for _, e := range entry.FindAllString(string(raw), -1) {
		nm := proname.FindStringSubmatch(e)
		if nm == nil {
			continue
		}
		v := "i"
		if pv := provolatile.FindStringSubmatch(e); pv != nil {
			v = pv[1]
		}
		if vols[nm[1]] == nil {
			vols[nm[1]] = map[string]bool{}
		}
		vols[nm[1]][v] = true
	}
	var want []string
	for n, vs := range vols {
		if !vs["i"] {
			want = append(want, n)
		}
	}
	sort.Strings(want)

	got := append([]string(nil), nonImmutableBuiltinNameList...)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("table has %d names, pg_proc.dat derives %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("name %d: table has %q, pg_proc.dat derives %q", i, got[i], want[i])
		}
	}
}
