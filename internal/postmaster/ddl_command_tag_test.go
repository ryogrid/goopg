package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestDDLCommandTagMatchesPostgres pins the wire-protocol CommandComplete
// tag ddlTag/commandTagFor produce for statement shapes that previously fell
// through to the generic "CREATE"/"ALTER"/"OK" placeholders instead of
// PostgreSQL's real per-object tag (postgres/src/include/tcop/cmdtaglist.h).
// Ledger: loop #41 (2026-07-01) first surfaced the CREATE OPERATOR FAMILY
// instance of this gap and deferred it as cosmetic; this closes the full
// class of sites (parser CompatNoopStmt literals + parseSkipToSemicolon
// default + the un-tagged CreateOpClassStmt/DropCompatStmt ddlTag cases).
func TestDDLCommandTagMatchesPostgres(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{"create operator class", `CREATE OPERATOR CLASS my_opc FOR TYPE int4 USING btree AS STORAGE int4`, "CREATE OPERATOR CLASS"},
		// 2026-09-23: kinds that fell through ddlTag/utilityTag to "OK"
		// (measured live against a private PG 18.3; tags per cmdtaglist.h).
		{"create function", `CREATE FUNCTION f() RETURNS int LANGUAGE sql AS 'select 1'`, "CREATE FUNCTION"},
		{"alter function", `ALTER FUNCTION f() STABLE`, "ALTER FUNCTION"},
		{"alter procedure", `ALTER PROCEDURE p() RENAME TO p2`, "ALTER PROCEDURE"},
		{"alter routine", `ALTER ROUTINE f() RENAME TO f2`, "ALTER ROUTINE"},
		{"drop function", `DROP FUNCTION f()`, "DROP FUNCTION"},
		{"create procedure", `CREATE PROCEDURE p() LANGUAGE sql AS 'select 1'`, "CREATE PROCEDURE"},
		{"drop procedure", `DROP PROCEDURE p()`, "DROP PROCEDURE"},
		{"create trigger", `CREATE TRIGGER tg BEFORE INSERT ON t FOR EACH ROW EXECUTE FUNCTION trgf()`, "CREATE TRIGGER"},
		{"drop trigger", `DROP TRIGGER tg ON t`, "DROP TRIGGER"},
		{"create event trigger", `CREATE EVENT TRIGGER et ON ddl_command_start EXECUTE FUNCTION f()`, "CREATE EVENT TRIGGER"},
		{"alter event trigger", `ALTER EVENT TRIGGER et DISABLE`, "ALTER EVENT TRIGGER"},
		{"create sequence", `CREATE SEQUENCE sq`, "CREATE SEQUENCE"},
		{"alter sequence", `ALTER SEQUENCE sq INCREMENT 2`, "ALTER SEQUENCE"},
		{"create rule", `CREATE RULE r AS ON UPDATE TO t DO INSTEAD NOTHING`, "CREATE RULE"},
		{"alter rule", `ALTER RULE r ON t RENAME TO r2`, "ALTER RULE"},
		{"drop rule", `DROP RULE r2 ON t`, "DROP RULE"},
		{"create policy", `CREATE POLICY pol ON t USING (true)`, "CREATE POLICY"},
		{"drop policy", `DROP POLICY pol ON t`, "DROP POLICY"},
		{"create matview with no data", `CREATE MATERIALIZED VIEW mv AS SELECT 1 AS x WITH NO DATA`, "CREATE MATERIALIZED VIEW"},
		{"refresh matview", `REFRESH MATERIALIZED VIEW mv`, "REFRESH MATERIALIZED VIEW"},
		{"ctas with no data", `CREATE TABLE ct AS SELECT 1 AS x WITH NO DATA`, "CREATE TABLE AS"},
		{"create publication", `CREATE PUBLICATION pub FOR TABLE t`, "CREATE PUBLICATION"},
		{"alter publication owner", `ALTER PUBLICATION pub OWNER TO postgres`, "ALTER PUBLICATION"},
		{"drop publication", `DROP PUBLICATION pub`, "DROP PUBLICATION"},
		{"create subscription", `CREATE SUBSCRIPTION s CONNECTION 'x' PUBLICATION p`, "CREATE SUBSCRIPTION"},
		{"alter subscription owner", `ALTER SUBSCRIPTION s OWNER TO postgres`, "ALTER SUBSCRIPTION"},
		{"drop subscription", `DROP SUBSCRIPTION s`, "DROP SUBSCRIPTION"},
		{"create access method", `CREATE ACCESS METHOD am2 TYPE TABLE HANDLER heap_tableam_handler`, "CREATE ACCESS METHOD"},
		{"alter operator set", `ALTER OPERATOR === (int4, int4) SET (RESTRICT = eqsel)`, "ALTER OPERATOR"},
		{"do", `DO 'begin null; end'`, "DO"},
		{"reindex", `REINDEX TABLE t`, "REINDEX"},
		{"cluster", `CLUSTER t`, "CLUSTER"},
		{"create operator family", `CREATE OPERATOR FAMILY my_opf USING btree`, "CREATE OPERATOR FAMILY"},
		{"create operator", `CREATE OPERATOR === (LEFTARG = int4, RIGHTARG = int4, FUNCTION = int4eq)`, "CREATE OPERATOR"},
		{"create conversion", `CREATE CONVERSION my_conv FOR 'UTF8' TO 'LATIN1' FROM my_conv_func`, "CREATE CONVERSION"},
		{"create default conversion", `CREATE DEFAULT CONVERSION my_dconv FOR 'UTF8' TO 'LATIN1' FROM my_conv_func`, "CREATE CONVERSION"},
		{"create text search dictionary", `CREATE TEXT SEARCH DICTIONARY my_dict (TEMPLATE = simple)`, "CREATE TEXT SEARCH DICTIONARY"},
		{"create text search configuration", `CREATE TEXT SEARCH CONFIGURATION my_cfg (PARSER = default)`, "CREATE TEXT SEARCH CONFIGURATION"},
		{"create server", `CREATE SERVER my_srv FOREIGN DATA WRAPPER my_fdw`, "CREATE SERVER"},
		{"create user mapping", `CREATE USER MAPPING FOR CURRENT_USER SERVER my_srv`, "CREATE USER MAPPING"},
		{"create foreign data wrapper", `CREATE FOREIGN DATA WRAPPER my_fdw`, "CREATE FOREIGN DATA WRAPPER"},
		{"alter foreign data wrapper", `ALTER FOREIGN DATA WRAPPER my_fdw OPTIONS (ADD opt 'v')`, "ALTER FOREIGN DATA WRAPPER"},
		{"drop operator class", `DROP OPERATOR CLASS my_opc USING btree`, "DROP OPERATOR CLASS"},
		{"drop operator family", `DROP OPERATOR FAMILY my_opf USING btree`, "DROP OPERATOR FAMILY"},
		{"drop conversion", `DROP CONVERSION my_conv`, "DROP CONVERSION"},
		{"drop sequence", `DROP SEQUENCE my_seq`, "DROP SEQUENCE"},
		{"drop schema", `DROP SCHEMA my_schema`, "DROP SCHEMA"},
		{"drop server", `DROP SERVER my_srv`, "DROP SERVER"},
		{"drop role", `DROP ROLE my_role`, "DROP ROLE"},
		{"drop user", `DROP USER my_user`, "DROP ROLE"},
		{"drop group", `DROP GROUP my_group`, "DROP ROLE"},
		{"drop text search parser", `DROP TEXT SEARCH PARSER my_parser`, "DROP TEXT SEARCH PARSER"},
		// DU-002 slice 439: ALTER SEQUENCE RENAME TO/OWNER TO/SET SCHEMA reuse
		// AlterTableStmt (a sequence is just a relation, mirroring PG's own
		// RenameRelation/AlterTableOwner/AlterTableNamespace), which would
		// otherwise tag as the generic "ALTER TABLE" via ddlTag's blanket
		// case — TagOverride corrects it to match PG's CreateCommandTag,
		// which tags by the statement's declared object type (OBJECT_SEQUENCE).
		{"alter sequence rename to", `ALTER SEQUENCE my_seq RENAME TO my_seq2`, "ALTER SEQUENCE"},
		{"alter sequence owner to", `ALTER SEQUENCE my_seq OWNER TO CURRENT_USER`, "ALTER SEQUENCE"},
		{"alter sequence set schema", `ALTER SEQUENCE my_seq SET SCHEMA my_schema`, "ALTER SEQUENCE"},
		// DU-002 slice 440: ALTER VIEW RENAME TO/OWNER TO/SET SCHEMA get the
		// same AlterTableStmt-reuse + TagOverride treatment as ALTER SEQUENCE
		// above (a view is just a relation too) — previously these fell into
		// the blanket "schema/view/collation/..." compat-stub loop, which
		// both mistagged AND silently discarded the change entirely.
		{"alter view rename to", `ALTER VIEW my_view RENAME TO my_view2`, "ALTER VIEW"},
		{"alter view owner to", `ALTER VIEW my_view OWNER TO CURRENT_USER`, "ALTER VIEW"},
		{"alter view set schema", `ALTER VIEW my_view SET SCHEMA my_schema`, "ALTER VIEW"},
		// DU-002 slice 443: ALTER INDEX ... RENAME TO and ALTER MATERIALIZED
		// VIEW ... SET SCHEMA both reuse AlterTableStmt (an index/matview is
		// just a relation too) but, unlike the slice 439/440 sequence/view
		// sites above, were never given a TagOverride — they fell through to
		// ddlTag's blanket "ALTER TABLE" default, exactly the mistagging gap
		// the slice 439 deferral predicted for these two forms.
		{"alter index rename to", `ALTER INDEX my_idx RENAME TO my_idx2`, "ALTER INDEX"},
		{"alter materialized view set schema", `ALTER MATERIALIZED VIEW my_mv SET SCHEMA my_schema`, "ALTER MATERIALIZED VIEW"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.sql, err)
			}
			if len(stmts) != 1 {
				t.Fatalf("parse %q: got %d statements, want 1", tc.sql, len(stmts))
			}
			if got := ddlTag(stmts[0]); got != tc.want {
				t.Errorf("ddlTag(%q) = %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}


type fakeDDLProcessed struct {
	executor.Operator
	n  int64
	ok bool
}

func (f fakeDDLProcessed) DDLProcessed() (int64, bool) { return f.n, f.ok }

// A populating CREATE TABLE AS / SELECT INTO / CREATE MATERIALIZED VIEW
// completes with PostgreSQL's `SELECT <n>` (createas.c:349, matview.c:389);
// the same statement WITH NO DATA keeps its DDL tag.
func TestCommandTagForPopulatingDDLIsSelectN(t *testing.T) {
	stmts, err := parser.Parse(`CREATE TABLE ct AS SELECT 1 AS x`)
	if err != nil {
		t.Fatal(err)
	}
	node := &optimizer.DDL{Stmt: stmts[0]}
	if got := commandTagFor(node, fakeDDLProcessed{n: 3, ok: true}, 0); got != "SELECT 3" {
		t.Fatalf("populating CTAS tag = %q, want SELECT 3", got)
	}
	if got := commandTagFor(node, fakeDDLProcessed{ok: false}, 0); got != "CREATE TABLE AS" {
		t.Fatalf("non-reporting CTAS tag = %q, want CREATE TABLE AS", got)
	}
}
