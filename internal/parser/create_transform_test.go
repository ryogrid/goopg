package parser

import "testing"

// TestParseCreateTransform covers CREATE TRANSFORM FOR type LANGUAGE lang
// (FROM SQL WITH FUNCTION ... [, TO SQL WITH FUNCTION ...] | ...). The parser
// must capture the type name, language, and each half's function name +
// explicit arg-type list so the executor can register the transform and
// resolve pg_transform.trffromsql/trftosql. PG's transform_element_list
// permits either half alone, or both in either order. DU-002 (M0119-0004).
func TestParseCreateTransform(t *testing.T) {
	cases := []struct {
		sql          string
		wantType     string
		wantLang     string
		wantFromName string
		wantFromArgs []string
		wantToName   string
		wantToArgs   []string
	}{
		{
			sql:      "CREATE TRANSFORM FOR int LANGUAGE SQL (FROM SQL WITH FUNCTION prsd_lextype(internal), TO SQL WITH FUNCTION int4recv(internal))",
			wantType: "int", wantLang: "sql",
			wantFromName: "prsd_lextype", wantFromArgs: []string{"internal"},
			wantToName: "int4recv", wantToArgs: []string{"internal"},
		},
		{
			// Reversed order: TO SQL first, then FROM SQL.
			sql:      "CREATE TRANSFORM FOR hstore LANGUAGE plpythonu (TO SQL WITH FUNCTION hstore_to_plpython(internal), FROM SQL WITH FUNCTION hstore_from_plpython(internal))",
			wantType: "hstore", wantLang: "plpythonu",
			wantFromName: "hstore_from_plpython", wantFromArgs: []string{"internal"},
			wantToName: "hstore_to_plpython", wantToArgs: []string{"internal"},
		},
		{
			// FROM SQL only — no args list.
			sql:      "CREATE TRANSFORM FOR int LANGUAGE sql (FROM SQL WITH FUNCTION myfromfn)",
			wantType: "int", wantLang: "sql",
			wantFromName: "myfromfn", wantFromArgs: nil,
			wantToName: "", wantToArgs: nil,
		},
		{
			// TO SQL only.
			sql:      "CREATE TRANSFORM FOR int LANGUAGE sql (TO SQL WITH FUNCTION mytofn(internal))",
			wantType: "int", wantLang: "sql",
			wantFromName: "", wantFromArgs: nil,
			wantToName: "mytofn", wantToArgs: []string{"internal"},
		},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("%q: unexpected parse error: %v", tc.sql, err)
			continue
		}
		if len(stmts) != 1 {
			t.Errorf("%q: got %d stmts, want 1", tc.sql, len(stmts))
			continue
		}
		ns, ok := stmts[0].(*CompatNoopStmt)
		if !ok {
			t.Errorf("%q: got %T, want *CompatNoopStmt", tc.sql, stmts[0])
			continue
		}
		if ns.ObjType != "transform" {
			t.Errorf("%q: ObjType=%q, want transform", tc.sql, ns.ObjType)
		}
		if ns.TransformType != tc.wantType {
			t.Errorf("%q: TransformType=%q, want %q", tc.sql, ns.TransformType, tc.wantType)
		}
		if ns.TransformLang != tc.wantLang {
			t.Errorf("%q: TransformLang=%q, want %q", tc.sql, ns.TransformLang, tc.wantLang)
		}
		if ns.TransformFromFunc.Name != tc.wantFromName {
			t.Errorf("%q: TransformFromFunc.Name=%q, want %q", tc.sql, ns.TransformFromFunc.Name, tc.wantFromName)
		}
		if len(ns.TransformFromArgs) != len(tc.wantFromArgs) {
			t.Errorf("%q: TransformFromArgs=%v, want %v", tc.sql, ns.TransformFromArgs, tc.wantFromArgs)
		}
		if ns.TransformToFunc.Name != tc.wantToName {
			t.Errorf("%q: TransformToFunc.Name=%q, want %q", tc.sql, ns.TransformToFunc.Name, tc.wantToName)
		}
		if len(ns.TransformToArgs) != len(tc.wantToArgs) {
			t.Errorf("%q: TransformToArgs=%v, want %v", tc.sql, ns.TransformToArgs, tc.wantToArgs)
		}
	}
}

// TestParseDropTransform covers DROP TRANSFORM [IF EXISTS] FOR type LANGUAGE
// lang [CASCADE|RESTRICT]. DU-002 (M0119-0004).
func TestParseDropTransform(t *testing.T) {
	cases := []struct {
		sql          string
		wantType     string
		wantLang     string
		wantIfExists bool
		wantBehavior DropBehavior
	}{
		{"DROP TRANSFORM FOR int LANGUAGE sql", "int", "sql", false, DropDefault},
		{"DROP TRANSFORM IF EXISTS FOR int LANGUAGE sql CASCADE", "int", "sql", true, DropCascade},
		{"DROP TRANSFORM FOR hstore LANGUAGE plpythonu RESTRICT", "hstore", "plpythonu", false, DropDefault},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("%q: unexpected parse error: %v", tc.sql, err)
			continue
		}
		if len(stmts) != 1 {
			t.Errorf("%q: got %d stmts, want 1", tc.sql, len(stmts))
			continue
		}
		ds, ok := stmts[0].(*DropCompatStmt)
		if !ok {
			t.Errorf("%q: got %T, want *DropCompatStmt", tc.sql, stmts[0])
			continue
		}
		if ds.ObjType != "transform" {
			t.Errorf("%q: ObjType=%q, want transform", tc.sql, ds.ObjType)
		}
		if ds.TransformType != tc.wantType {
			t.Errorf("%q: TransformType=%q, want %q", tc.sql, ds.TransformType, tc.wantType)
		}
		if ds.TransformLang != tc.wantLang {
			t.Errorf("%q: TransformLang=%q, want %q", tc.sql, ds.TransformLang, tc.wantLang)
		}
		if ds.IfExists != tc.wantIfExists {
			t.Errorf("%q: IfExists=%v, want %v", tc.sql, ds.IfExists, tc.wantIfExists)
		}
		if ds.Behavior != tc.wantBehavior {
			t.Errorf("%q: Behavior=%v, want %v", tc.sql, ds.Behavior, tc.wantBehavior)
		}
	}
}
