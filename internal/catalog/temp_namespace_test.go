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
