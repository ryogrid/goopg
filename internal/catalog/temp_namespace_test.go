package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestTempNamespaceLifecycle exercises the per-session temporary-namespace
// registry that backs pg_my_temp_schema() and the pg_temp_<id> namespace row
// (design 0118-0091, M0118-0009 temp-schema-cleanup).
func TestTempNamespaceLifecycle(t *testing.T) {
	c := NewInMemory()

	// No namespace until one is established.
	if oid := c.TempNamespaceOID("s7"); oid != 0 {
		t.Fatalf("TempNamespaceOID before Ensure = %d, want 0", oid)
	}
	// A blank owner (session-less context) never gets a namespace.
	if oid := c.EnsureTempNamespace(""); oid != 0 {
		t.Fatalf("EnsureTempNamespace(\"\") = %d, want 0", oid)
	}

	// Establishing one returns a stable, non-zero OID.
	oid := c.EnsureTempNamespace("s7")
	if oid == 0 {
		t.Fatal("EnsureTempNamespace(s7) returned 0")
	}
	if got := c.EnsureTempNamespace("s7"); got != oid {
		t.Fatalf("EnsureTempNamespace is not idempotent: %d != %d", got, oid)
	}
	if got := c.TempNamespaceOID("s7"); got != oid {
		t.Fatalf("TempNamespaceOID(s7) = %d, want %d", got, oid)
	}
	// A different session gets a distinct OID.
	if other := c.EnsureTempNamespace("s8"); other == oid {
		t.Fatalf("two sessions share a temp namespace OID %d", oid)
	}

	// The namespace surfaces in pg_namespace as pg_temp_<id>.
	c.mu.RLock()
	schemas := c.allSchemasLocked()
	c.mu.RUnlock()
	var found bool
	for _, s := range schemas {
		if s.oid == oid {
			found = true
			if s.name != "pg_temp_7" {
				t.Fatalf("temp namespace name = %q, want pg_temp_7", s.name)
			}
		}
	}
	if !found {
		t.Fatalf("temp namespace OID %d not present in allSchemasLocked", oid)
	}

	// Dropping the namespace removes it; the OID no longer resolves.
	c.DropTempNamespace("s7")
	if got := c.TempNamespaceOID("s7"); got != 0 {
		t.Fatalf("TempNamespaceOID after drop = %d, want 0", got)
	}
}

// TestDropSessionTempObjects verifies DISCARD-TEMP / session-exit cleanup drops
// only the calling session's temporary relations and leaves the namespace and
// other sessions' objects intact.
func TestDropSessionTempObjects(t *testing.T) {
	c := NewInMemory()
	c.EnsureTempNamespace("s1")

	mkTemp := func(name, owner string, oid uint32) {
		c.mu.Lock()
		c.tables[name] = &Table{OID: oid, Name: name, Temp: true, TempOwner: owner}
		c.mu.Unlock()
	}
	mkTemp("t_s1_a", "s1", 20001)
	mkTemp("t_s1_b", "s1", 20002)
	mkTemp("t_s2_a", "s2", 20003)
	// A permanent table must never be touched.
	c.mu.Lock()
	c.tables["perm"] = &Table{OID: 20004, Name: "perm"}
	c.mu.Unlock()

	if n := c.DropSessionTempObjects("s1"); n != 2 {
		t.Fatalf("DropSessionTempObjects(s1) dropped %d, want 2", n)
	}

	for _, gone := range []string{"t_s1_a", "t_s1_b"} {
		if _, ok := c.LookupTable(parser.ObjectName{Name: gone}); ok {
			t.Fatalf("temp table %q still present after DISCARD TEMP", gone)
		}
	}
	if _, ok := c.LookupTable(parser.ObjectName{Name: "t_s2_a"}); !ok {
		t.Fatal("other session's temp table was dropped")
	}
	if _, ok := c.LookupTable(parser.ObjectName{Name: "perm"}); !ok {
		t.Fatal("permanent table was dropped")
	}
	// The namespace itself persists (PostgreSQL reuses pg_temp_N).
	if oid := c.TempNamespaceOID("s1"); oid == 0 {
		t.Fatal("DropSessionTempObjects removed the namespace; it should persist")
	}
}

// TestSessionTempTableNamesAndTypeCascade verifies the temp-type dependency
// cascade backing DISCARD TEMP / backend exit: SessionTempTableNames lists the
// session's temp relations by name, and Routines.DropRoutinesReferencingTypes
// drops exactly the routines that take or return one of those rowtypes —
// mirroring PostgreSQL cascading a temp table's composite type to dependent
// functions (the temp-schema-cleanup spec's uses_a_temp_type). M0118-0009.
func TestSessionTempTableNamesAndTypeCascade(t *testing.T) {
	c := NewInMemory()
	c.EnsureTempNamespace("s1")
	c.mu.Lock()
	c.tables["just_give_me_a_type"] = &Table{OID: 30001, Name: "just_give_me_a_type", Temp: true, TempOwner: "s1"}
	c.tables["other_temp"] = &Table{OID: 30002, Name: "other_temp", Temp: true, TempOwner: "s1"}
	c.tables["s2_temp"] = &Table{OID: 30003, Name: "s2_temp", Temp: true, TempOwner: "s2"}
	c.mu.Unlock()

	names := c.SessionTempTableNames("s1")
	if len(names) != 2 {
		t.Fatalf("SessionTempTableNames(s1) = %v, want 2 names", names)
	}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if !got["just_give_me_a_type"] || !got["other_temp"] {
		t.Fatalf("SessionTempTableNames(s1) = %v, missing expected names", names)
	}
	if got["s2_temp"] {
		t.Fatalf("SessionTempTableNames(s1) leaked another session's temp table: %v", names)
	}

	rs := NewRoutines()
	// uses_a_temp_type(just_give_me_a_type) — depends on the temp rowtype (arg).
	if _, err := rs.Create(&Routine{Schema: "public", Name: "uses_a_temp_type",
		ArgTypes: []Type{{Name: "just_give_me_a_type"}}, ReturnType: Type{Name: "int4"}}, false); err != nil {
		t.Fatalf("Create uses_a_temp_type: %v", err)
	}
	// returns_temp_type() — depends on the temp rowtype (return).
	if _, err := rs.Create(&Routine{Schema: "public", Name: "returns_temp_type",
		ReturnType: Type{Name: "other_temp"}}, false); err != nil {
		t.Fatalf("Create returns_temp_type: %v", err)
	}
	// unrelated(int4) — must survive.
	if _, err := rs.Create(&Routine{Schema: "public", Name: "unrelated",
		ArgTypes: []Type{{Name: "int4"}}, ReturnType: Type{Name: "int4"}}, false); err != nil {
		t.Fatalf("Create unrelated: %v", err)
	}

	dropped := rs.DropRoutinesReferencingTypes(names)
	if len(dropped) != 2 {
		t.Fatalf("DropRoutinesReferencingTypes dropped %d routines, want 2", len(dropped))
	}
	if _, ok := rs.Lookup(parser.ObjectName{Schema: "public", Name: "uses_a_temp_type"}, []Type{{Name: "just_give_me_a_type"}}); ok {
		t.Fatal("uses_a_temp_type survived the temp-type cascade")
	}
	if _, ok := rs.Lookup(parser.ObjectName{Schema: "public", Name: "unrelated"}, []Type{{Name: "int4"}}); !ok {
		t.Fatal("unrelated routine was wrongly cascade-dropped")
	}
}
