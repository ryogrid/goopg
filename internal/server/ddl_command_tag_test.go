package server

import (
	"testing"

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
