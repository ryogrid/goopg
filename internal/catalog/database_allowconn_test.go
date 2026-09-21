package catalog

import "testing"

// TestDatabaseAllowsConnectionsMatchesPGDatabaseRow pins the AGREEMENT between
// the two places that decide whether a database accepts connections: the
// pg_database.datallowconn column a client reads, and the accessor the
// postmaster's connect-time gate calls.
//
// They must never disagree, and before this was wired they did: the catalog
// reported datallowconn = false for template0 (pg_amcheck's `--all` filter
// reads it and correctly skipped the database) while the postmaster accepted
// `psql -d template0` anyway. A catalog that says "closed" beside a server that
// says "come in" is the worst version of this bug, because every tool that asks
// politely is told the truth and every tool that just connects is not.
//
// The test reads the ACTUAL rendered rows rather than restating the rule, so a
// future edit to either side has to keep them consistent.
func TestDatabaseAllowsConnectionsMatchesPGDatabaseRow(t *testing.T) {
	c := NewInMemory()
	tbl := c.ns(DefaultDBOid).tables["pg_catalog.pg_database"]
	if tbl == nil || tbl.VirtualRows == nil {
		t.Fatal("pg_database virtual table not registered")
	}
	const (
		colDatname      = 1
		colDatallowconn = 4
	)
	rows := tbl.VirtualRows()
	if len(rows) == 0 {
		t.Fatal("pg_database rendered no rows")
	}
	sawTemplate0 := false
	for _, r := range rows {
		if len(r) <= colDatallowconn {
			t.Fatalf("pg_database row too short: %v", r)
		}
		name := r[colDatname]
		rowSaysAllowed := r[colDatallowconn] == "true"
		if got := c.DatabaseAllowsConnections(name); got != rowSaysAllowed {
			t.Errorf("database %q: DatabaseAllowsConnections = %v but "+
				"pg_database.datallowconn = %q — the connect gate and the "+
				"catalog must not disagree", name, got, r[colDatallowconn])
		}
		if name == "template0" {
			sawTemplate0 = true
			if rowSaysAllowed {
				t.Error("template0 is reported connectable; PostgreSQL seeds it " +
					"datallowconn = false so it stays a pristine CREATE DATABASE template")
			}
		}
	}
	if !sawTemplate0 {
		t.Fatal("no template0 row — the case this test exists for was not exercised")
	}
	// template1 IS connectable in PostgreSQL; that is the point of the
	// two-template design. Guard against "fix" by blanket-refusing templates.
	if !c.DatabaseAllowsConnections("template1") {
		t.Error("template1 reported unconnectable — PostgreSQL allows it, and it " +
			"is the template users are meant to customise")
	}
	if !c.DatabaseAllowsConnections("postgres") {
		t.Error("postgres reported unconnectable")
	}
}
