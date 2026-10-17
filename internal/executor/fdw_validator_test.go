package executor

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPostgresqlFDWValidator pins M0122-0015g: VALIDATOR
// postgresql_fdw_validator resolves to the built-in (it is no user routine),
// and its option check runs for the wrapper, server, user mapping and foreign
// table, as transformGenericOptions does. Messages and hints are PG 18.3's
// (foreign.c postgresql_fdw_validator, varlena.c closest match).
func TestPostgresqlFDWValidator(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	mustOK := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustFail := func(sql, msg, hint string) {
		t.Helper()
		err := runDDL(t, ctx, sql)
		var ee *ExecError
		if !errors.As(err, &ee) || ee.Code != "42601" || ee.Message != msg || ee.Hint != hint {
			t.Errorf("%s: err = %v (hint %q); want 42601 %q / %q", sql, err, func() string {
				if ee != nil {
					return ee.Hint
				}
				return ""
			}(), msg, hint)
		}
	}
	const none = "There are no valid options in this context."
	mustOK(`CREATE FOREIGN DATA WRAPPER postgresql VALIDATOR postgresql_fdw_validator`)
	if fdw, ok := im.LookupForeignDataWrapper("postgresql"); !ok || fdw.ValidatorOID != postgresqlFDWValidatorOID {
		t.Fatalf("validator not resolved to the built-in: %+v", fdw)
	}
	mustFail(`CREATE FOREIGN DATA WRAPPER foo VALIDATOR pg_catalog.postgresql_fdw_validator OPTIONS (bad '1')`, `invalid option "bad"`, none)
	mustFail(`CREATE SERVER s8 FOREIGN DATA WRAPPER postgresql OPTIONS (foo '1')`, `invalid option "foo"`, "")
	mustFail(`CREATE SERVER s8 FOREIGN DATA WRAPPER postgresql OPTIONS (hots 'x')`, `invalid option "hots"`, `Perhaps you meant the option "host".`)
	mustFail(`CREATE SERVER s8 FOREIGN DATA WRAPPER postgresql OPTIONS (user 'x')`, `invalid option "user"`, "")
	mustOK(`CREATE SERVER s8 FOREIGN DATA WRAPPER postgresql OPTIONS (host 'localhost', dbname 's8db')`)
	mustFail(`CREATE USER MAPPING FOR current_user SERVER s8 OPTIONS (username 'test')`, `invalid option "username"`, `Perhaps you meant the option "user".`)
	mustOK(`CREATE USER MAPPING FOR current_user SERVER s8 OPTIONS (user 'test', password 'secret')`)
	mustFail(`CREATE FOREIGN TABLE ft1 (a int) SERVER s8 OPTIONS (table_name 't')`, `invalid option "table_name"`, none)
	mustOK(`CREATE FOREIGN TABLE ft1 (a int) SERVER s8`)
	mustFail(`ALTER FOREIGN TABLE ft1 OPTIONS (ADD x '1')`, `invalid option "x"`, none)

	// A failing ALTER changes nothing: the validator switch and the options
	// are applied together, after validation.
	mustOK(`CREATE FOREIGN DATA WRAPPER foo`)
	mustOK(`ALTER FOREIGN DATA WRAPPER foo OPTIONS (keep '1')`)
	mustFail(`ALTER FOREIGN DATA WRAPPER foo VALIDATOR postgresql_fdw_validator OPTIONS (ADD another 'x')`, `invalid option "keep"`, none)
	if fdw, _ := im.LookupForeignDataWrapper("foo"); fdw.ValidatorOID != 0 || len(fdw.Options) != 1 {
		t.Errorf("failed ALTER left validator %d options %v; want unchanged", fdw.ValidatorOID, fdw.Options)
	}
}

// TestClosestMatch pins varlena.c's closest-match rules: distance at most
// maxD and at most half the source length, first strictly closest wins.
func TestClosestMatch(t *testing.T) {
	opts := []string{"authtype", "service", "connect_timeout", "dbname", "host", "hostaddr", "port"}
	for _, c := range []struct{ src, want string }{
		{"hots", "host"},
		{"dbnam", "dbname"},
		{"foo", ""},      // nothing within half its length
		{"prot", "port"}, // two substitutions, half of 4
		{"", ""},
	} {
		if got := closestMatch(c.src, opts, 4); got != c.want {
			t.Errorf("closestMatch(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}
