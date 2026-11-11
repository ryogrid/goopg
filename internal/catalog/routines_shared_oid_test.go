package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestRoutineOIDsShareTheCatalogCounter pins M0148-0001j4 item (1): PG takes
// pg_proc, pg_class and pg_type OIDs from one counter (GetNewObjectId), so a
// function created between two tables gets an OID between theirs and
// findDependentObjects' OID ordering lists objects in creation order. A
// separate routine counter (FirstRoutineOID upward) put every function above
// every table. CREATE OR REPLACE keeps its OID and draws none, a routine
// recovered from pg_proc advances the shared counter, and a bare NewRoutines
// registry keeps its own counter.
func TestRoutineOIDsShareTheCatalogCounter(t *testing.T) {
	c := NewInMemory()
	t1, err := c.CreateTable(parser.ObjectName{Name: "t1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rs := c.Routines()
	f, err := rs.Create(makeRoutine("public", "f", nil, Type{Name: "int"}, "BEGIN END"), false)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := c.CreateTable(parser.ObjectName{Name: "t2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !(t1.OID < f.OID && f.OID < t2.OID) {
		t.Fatalf("OIDs t1=%d f=%d t2=%d; want the function between the tables", t1.OID, f.OID, t2.OID)
	}

	before := c.NextOID()
	g, err := rs.Create(makeRoutine("public", "f", nil, Type{Name: "int"}, "BEGIN RETURN 1; END"), true)
	if err != nil {
		t.Fatal(err)
	}
	if g.OID != f.OID || c.NextOID() != before {
		t.Errorf("CREATE OR REPLACE: oid %d (want %d), counter %d→%d (want unchanged)", g.OID, f.OID, before, c.NextOID())
	}
	if _, err := rs.Create(makeRoutine("public", "f", nil, Type{Name: "int"}, "BEGIN END"), false); err == nil || c.NextOID() != before {
		t.Errorf("duplicate CREATE: err=%v, counter %d→%d (want an error and no OID drawn)", err, before, c.NextOID())
	}

	rec := makeRoutine("public", "recovered", nil, Type{Name: "int"}, "BEGIN END")
	rec.OID = c.NextOID() + 100
	rs.CreateDuringRecovery(rec)
	if next := c.AllocOID(); next <= rec.OID {
		t.Errorf("after recovering routine oid %d the catalog allocated %d; want a higher OID", rec.OID, next)
	}

	bare := NewRoutines()
	r, err := bare.Create(makeRoutine("public", "h", nil, Type{Name: "int"}, "BEGIN END"), false)
	if err != nil || r.OID != FirstRoutineOID {
		t.Errorf("standalone registry: oid=%d err=%v; want FirstRoutineOID", r.OID, err)
	}
}
