package executor

import (
	"strings"
	"testing"
)

// TestResolvePrepareParamType pins typenameTypeId for PREPARE's declared
// types (M0122-0015c): pg_dump's `PREPARE dumpFunc(pg_catalog.oid)` was 42704
// because the validator was a bare-name allowlist, so pg_dump of any database
// holding a user function failed. Expected outcomes are PG 18.3's for the same
// PREPAREs.
func TestResolvePrepareParamType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, sql := range []string{
		`CREATE TYPE mood AS ENUM ('sad', 'ok')`,
		`CREATE TABLE prept (a int, b text)`,
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	builtin := func(s string) bool {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "oid", "int4", "int4[]", "text", "varchar(3)", `"char"`:
			return true
		}
		return false
	}
	for _, c := range []struct{ in, want, code string }{
		{"pg_catalog.oid", "oid", ""},
		{"PG_CATALOG.int4[]", "int4[]", ""},
		{"pg_catalog.varchar(3)", "varchar(3)", ""},
		{`pg_catalog."char"`, `"char"`, ""},
		{"oid", "oid", ""},
		{"mood", "mood", ""},
		{"public.mood", "mood", ""},
		{"public.prept", "prept", ""},
		{"pg_catalog.mood", "", "42704"}, // built-ins only answer to pg_catalog, user types never do
		{"public.int4", "", "42704"},     // ... and a built-in is not in public
		{"nosuch.int4", "", "3F000"},     // the schema is resolved first
		{"pg_catalog.nosuch", "", "42704"},
		{"nosuchtype", "", "42704"},
	} {
		got, err := ResolvePrepareParamType(ctx, c.in, builtin)
		switch {
		case c.code == "" && (err != nil || got != c.want):
			t.Errorf("%s: got %q, %v; want %q", c.in, got, err, c.want)
		case c.code != "" && (err == nil || err.Code != c.code):
			t.Errorf("%s: err %v; want %s", c.in, err, c.code)
		}
	}
	if _, err := ResolvePrepareParamType(ctx, "nosuch.int4", builtin); err == nil || err.Message != `schema "nosuch" does not exist` {
		t.Errorf("3F000 message: %v", err)
	}
	if _, err := ResolvePrepareParamType(ctx, "pg_catalog.nosuch", builtin); err == nil || err.Message != `type "pg_catalog.nosuch" does not exist` {
		t.Errorf("42704 message: %v", err)
	}
}
