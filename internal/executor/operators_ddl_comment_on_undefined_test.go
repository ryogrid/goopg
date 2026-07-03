package executor

// operators_ddl_comment_on_undefined_test.go pins the DU-002 slice 436 fix:
// COMMENT ON <object> for a name that doesn't resolve now raises the same
// "does not exist" error real PostgreSQL raises (SQLSTATE + wording sourced
// from the upstream get_object_address family in
// postgres/src/backend/catalog/objectaddress.c and its per-object-kind
// lookup helpers), instead of silently succeeding with no pg_description
// row written. Before this slice every case in execCommentOn's switch
// returned nil on an unresolved lookup.

import (
	"errors"
	"testing"
)

func TestCommentOnUndefinedObjectErrors(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		code    string
		message string
	}{
		{"table", `COMMENT ON TABLE nosuchtbl IS 'x'`, "42P01", `relation "nosuchtbl" does not exist`},
		{"view", `COMMENT ON VIEW nosuchview IS 'x'`, "42P01", `relation "nosuchview" does not exist`},
		{"sequence", `COMMENT ON SEQUENCE nosuchseq IS 'x'`, "42P01", `relation "nosuchseq" does not exist`},
		{"materialized view", `COMMENT ON MATERIALIZED VIEW nosuchmv IS 'x'`, "42P01", `relation "nosuchmv" does not exist`},
		{"foreign table", `COMMENT ON FOREIGN TABLE nosuchft IS 'x'`, "42P01", `relation "nosuchft" does not exist`},
		{"index", `COMMENT ON INDEX nosuchidx IS 'x'`, "42P01", `relation "nosuchidx" does not exist`},
		{"schema", `COMMENT ON SCHEMA nosuchschema IS 'x'`, "3F000", `schema "nosuchschema" does not exist`},
		{"type", `COMMENT ON TYPE nosuchtype IS 'x'`, "42704", `type "nosuchtype" does not exist`},
		{"domain", `COMMENT ON DOMAIN nosuchdomain IS 'x'`, "42704", `type "nosuchdomain" does not exist`},
		{"access method", `COMMENT ON ACCESS METHOD nosuchmethod IS 'x'`, "42704", `access method "nosuchmethod" does not exist`},
		{"server", `COMMENT ON SERVER nosuchsrv IS 'x'`, "42704", `server "nosuchsrv" does not exist`},
		{"foreign data wrapper", `COMMENT ON FOREIGN DATA WRAPPER nosuchfdw IS 'x'`, "42704", `foreign-data wrapper "nosuchfdw" does not exist`},
		{"extension", `COMMENT ON EXTENSION nosuchext IS 'x'`, "42704", `extension "nosuchext" does not exist`},
		{"collation", `COMMENT ON COLLATION nosuchcoll IS 'x'`, "42704", `collation "nosuchcoll" for encoding "UTF8" does not exist`},
		{"cast", `COMMENT ON CAST (integer AS point) IS 'x'`, "42704", `cast from type integer to type point does not exist`},
		{"statistics", `COMMENT ON STATISTICS nosuchstat IS 'x'`, "42704", `statistics object "nosuchstat" does not exist`},
		{"function", `COMMENT ON FUNCTION nosuchfunc(integer) IS 'x'`, "42883", `function nosuchfunc(integer) does not exist`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, cleanup := newDDLFixture(t)
			defer cleanup()

			err := runDDL(t, ctx, tc.sql)
			var ee *ExecError
			if !errors.As(err, &ee) {
				t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
			}
			if ee.Code != tc.code {
				t.Errorf("Code=%q want %q", ee.Code, tc.code)
			}
			if ee.Message != tc.message {
				t.Errorf("Message=%q want %q", ee.Message, tc.message)
			}
		})
	}
}

// TestCommentOnUndefinedRelationSubObjectErrors covers the four object kinds
// keyed on an existing table (column/constraint/trigger/policy/rule): each
// must raise "relation ... does not exist" (42P01) when the table itself is
// missing, and its own specific "does not exist" error when the table exists
// but the named sub-object does not.
func TestCommentOnUndefinedRelationSubObjectErrors(t *testing.T) {
	missingTableCases := []struct {
		name string
		sql  string
	}{
		{"column", `COMMENT ON COLUMN nosuchtbl.a IS 'x'`},
		{"constraint", `COMMENT ON CONSTRAINT nosuchcons ON nosuchtbl IS 'x'`},
		{"trigger", `COMMENT ON TRIGGER nosuchtrig ON nosuchtbl IS 'x'`},
		{"policy", `COMMENT ON POLICY nosuchpol ON nosuchtbl IS 'x'`},
		{"rule", `COMMENT ON RULE nosuchrule ON nosuchtbl IS 'x'`},
	}
	for _, tc := range missingTableCases {
		t.Run(tc.name+"/missing table", func(t *testing.T) {
			ctx, _, cleanup := newDDLFixture(t)
			defer cleanup()

			err := runDDL(t, ctx, tc.sql)
			var ee *ExecError
			if !errors.As(err, &ee) {
				t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
			}
			if ee.Code != "42P01" {
				t.Errorf("Code=%q want 42P01", ee.Code)
			}
			want := `relation "nosuchtbl" does not exist`
			if ee.Message != want {
				t.Errorf("Message=%q want %q", ee.Message, want)
			}
		})
	}

	missingSubObjectCases := []struct {
		name    string
		sql     string
		code    string
		message string
	}{
		{"column", `COMMENT ON COLUMN t1.nosuchcol IS 'x'`, "42703", `column "nosuchcol" of relation "t1" does not exist`},
		{"constraint", `COMMENT ON CONSTRAINT nosuchcons ON t1 IS 'x'`, "42704", `constraint "nosuchcons" for table "t1" does not exist`},
		{"trigger", `COMMENT ON TRIGGER nosuchtrig ON t1 IS 'x'`, "42704", `trigger "nosuchtrig" for table "t1" does not exist`},
		{"policy", `COMMENT ON POLICY nosuchpol ON t1 IS 'x'`, "42704", `policy "nosuchpol" for table "t1" does not exist`},
		{"rule", `COMMENT ON RULE nosuchrule ON t1 IS 'x'`, "42704", `rule "nosuchrule" for relation "t1" does not exist`},
	}
	for _, tc := range missingSubObjectCases {
		t.Run(tc.name+"/missing sub-object", func(t *testing.T) {
			ctx, _, cleanup := newDDLFixture(t)
			defer cleanup()

			if err := runDDL(t, ctx, `CREATE TABLE t1 (a int)`); err != nil {
				t.Fatalf("CREATE TABLE: %v", err)
			}
			err := runDDL(t, ctx, tc.sql)
			var ee *ExecError
			if !errors.As(err, &ee) {
				t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
			}
			if ee.Code != tc.code {
				t.Errorf("Code=%q want %q", ee.Code, tc.code)
			}
			if ee.Message != tc.message {
				t.Errorf("Message=%q want %q", ee.Message, tc.message)
			}
		})
	}
}
