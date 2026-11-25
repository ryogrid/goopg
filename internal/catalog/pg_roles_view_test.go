package catalog

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPgRolesIsPgAuthidProjection pins system_views.sql's pg_roles: the
// view's 13 columns in PG order (psql's \du reads rolinherit /
// rolcreaterole / rolconnlimit / rolvaliduntil / rolbypassrls by name) and
// one row per pg_authid row, with the password masked.
func TestPgRolesIsPgAuthidProjection(t *testing.T) {
	c := NewInMemory()
	roles, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_roles"})
	if !ok {
		t.Fatal("pg_roles missing")
	}
	var names []string
	for _, col := range roles.Columns {
		names = append(names, col.Name)
	}
	want := "rolname,rolsuper,rolinherit,rolcreaterole,rolcreatedb,rolcanlogin,rolreplication,rolconnlimit,rolpassword,rolvaliduntil,rolbypassrls,rolconfig,oid"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("pg_roles columns = %s\nwant %s", got, want)
	}
	authid, _ := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_authid"})
	ar, rr := authid.VirtualRows(), roles.VirtualRows()
	if len(ar) != len(rr) || len(rr) == 0 {
		t.Fatalf("pg_roles rows = %d, pg_authid rows = %d", len(rr), len(ar))
	}
	for i := range rr {
		if rr[i][0] != ar[i][1] || rr[i][12] != ar[i][0] || rr[i][8] != "********" || rr[i][11] != VirtualNull {
			t.Errorf("row %d = %q (pg_authid %q)", i, rr[i], ar[i])
		}
	}
}
