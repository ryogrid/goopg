package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0134-0168: `'<name>'::regclass` must BE regclassin
// (postgres/src/backend/utils/adt/regproc.c:960-1000), not a best-effort
// lookup that returns the raw name when it misses.
//
// The bug this pins: evalExprSlot's *optimizer.CastExpr regclass arm
// re-implemented the parse-and-look-up inline and simply FELL THROUGH on a
// miss, so `'nosuch'::regclass` evaluated to the KindString "nosuch" — a
// regclass value naming a relation that does not exist. Nothing rejected it;
// the nonsense only surfaced downstream, e.g. psql's `\sv` casts the resolved
// regclass on to oid and got `invalid input syntax for type oid: "<name>"`
// (22P02) where PG raises `relation "<name>" does not exist` (42P01) at the
// cast itself. sqljson.sql exposed it, but the shape is engine-wide: every
// `'x'::regclass` in pg_dump/psql/ACL/size-function SQL took the same arm.
//
// The fix routes that arm through regIdentifierInput — the shared reg*in port
// the heap-write, reg*[] element and EXECUTE-parameter paths already used —
// which makes the two siblings one implementation (Hard-won Rule #2) and
// additionally supplies parseDashOrOid (regproc.c:1865) and
// makeRangeVarFromNameList's segment-count rules (namespace.c:3556-3580).
//
// Every want below is the byte-exact PG 18.3 answer, captured live from the
// oracle under ./postgres/ while writing this test.
func TestRegclassStringInputIsRegclassin(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	// The fixture leaves CurrentDatabase empty; a real connection never does,
	// and the three-segment catalog.schema.relation rule below compares
	// against it.
	if ctx.CurrentDatabase == "" {
		ctx.CurrentDatabase = "postgres"
	}
	if err := runDDL(t, ctx, `CREATE TABLE t168 (a int)`); err != nil {
		t.Fatalf("CREATE TABLE t168: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE INDEX i168 ON t168 (a)`); err != nil {
		t.Fatalf("CREATE INDEX i168: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t168"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal("t168 not found")
	}

	// ── Hits resolve to the OID, not to the name string ──────────────────
	// A KindString result here is the pre-fix behaviour: it renders the same
	// but compares as text, so `oid = 't'::regclass` silently matched nothing.
	for _, tc := range []struct {
		query string
		want  int64
	}{
		{`SELECT 't168'::regclass`, int64(tbl.OID)},
		{`SELECT 'public.t168'::regclass`, int64(tbl.OID)},
		// makeRangeVarFromNameList accepts catalog.schema.relation when the
		// catalog is the current database (namespace.c:3572).
		{`SELECT '` + ctx.CurrentDatabase + `.public.t168'::regclass`, int64(tbl.OID)},
		// parseDashOrOid (regproc.c:1865): "-" is InvalidOid and a pure-digit
		// string is a numeric OID — neither is a name to resolve, so neither
		// may raise the miss error below.
		{`SELECT '-'::regclass`, 0},
		{`SELECT '12345'::regclass`, 12345},
	} {
		rows := runQuery(t, ctx, tc.query)
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Errorf("%s: got %d rows, want 1", tc.query, len(rows))
			continue
		}
		got := rows[0][0]
		if got.Kind != KindInt || got.Int != tc.want {
			t.Errorf("%s = %+v, want KindInt %d", tc.query, got, tc.want)
		}
	}

	// An index name resolves too (regclassin looks up any pg_class row).
	if rows := runQuery(t, ctx, `SELECT 'i168'::regclass`); len(rows) != 1 ||
		rows[0][0].Kind != KindInt || rows[0][0].Int == 0 {
		t.Errorf(`SELECT 'i168'::regclass = %+v, want a non-zero KindInt OID`, rows)
	}

	// ── Misses raise upstream's error, they do not pass the name through ──
	for _, tc := range []struct {
		query string
		code  string
		msg   string
	}{
		// RangeVarGetRelid(..., missing_ok=false), regproc.c:989.
		{`SELECT 'nosuch168'::regclass`, "42P01", `relation "nosuch168" does not exist`},
		{`SELECT 'nosuchschema.t168'::regclass`, "42P01", `relation "nosuchschema.t168" does not exist`},
		// The miss message prints the PARSED name: quotes are stripped and an
		// unquoted segment keeps its case exactly as SplitIdentifierString left
		// it (NameListToString).
		{`SELECT '"T168"'::regclass`, "42P01", `relation "T168" does not exist`},
		// makeRangeVarFromNameList, namespace.c:3576.
		{`SELECT 'a.b.c.d'::regclass`, "42601", "improper relation name (too many dotted names): a.b.c.d"},
		// RangeVarGetRelidExtended, namespace.c:455-462.
		{`SELECT 'otherdb.public.t168'::regclass`, "0A000", `cross-database references are not implemented: "otherdb.public.t168"`},
		// stringToQualifiedNameList, regproc.c:1813.
		{`SELECT ''::regclass`, "42602", "invalid name syntax"},
	} {
		_, err := runQueryWithErr(ctx, tc.query)
		if err == nil {
			t.Errorf("%s: no error, want %s %q", tc.query, tc.code, tc.msg)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("%s: err %T (%v), want *ExecError", tc.query, err, err)
			continue
		}
		if ee.Code != tc.code || !strings.Contains(ee.Message, tc.msg) {
			t.Errorf("%s: %s %q, want %s %q", tc.query, ee.Code, ee.Message, tc.code, tc.msg)
		}
	}
}
