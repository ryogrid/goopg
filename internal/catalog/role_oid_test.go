package catalog

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestRoleOIDRegistry pins the per-role OID registry that lets named-role
// policies round-trip through pg_dump (DU-002 slice 330): CREATE ROLE mints a
// stable OID, RoleOID resolves it (and the seeded `postgres` superuser), and
// pg_roles exposes every registered role so pg_dump's getPolicies can map a
// pg_policy.polroles OID back to the role name.
func TestRoleOIDRegistry(t *testing.T) {
	c := NewInMemory()

	// The seeded bootstrap superuser resolves to OID 10 without registration.
	if oid, ok := c.RoleOID("postgres"); !ok || oid != 10 {
		t.Fatalf("RoleOID(postgres) = (%d,%v); want (10,true)", oid, ok)
	}
	// Case-insensitive.
	if oid, ok := c.RoleOID("POSTGRES"); !ok || oid != 10 {
		t.Fatalf("RoleOID(POSTGRES) = (%d,%v); want (10,true)", oid, ok)
	}
	// An unregistered role is unknown.
	if _, ok := c.RoleOID("nobody"); ok {
		t.Fatalf("RoleOID(nobody) reported a known role")
	}

	c.RegisterRole("pol_role")
	oid, ok := c.RoleOID("pol_role")
	if !ok || oid == 0 {
		t.Fatalf("RoleOID(pol_role) = (%d,%v); want a nonzero OID", oid, ok)
	}
	// Lookup is case-insensitive on the stored name.
	if got, ok := c.RoleOID("POL_ROLE"); !ok || got != oid {
		t.Fatalf("RoleOID(POL_ROLE) = (%d,%v); want (%d,true)", got, ok, oid)
	}
	// Re-registering keeps the OID stable (a policy's polroles entry must stay
	// valid across the session).
	c.RegisterRole("pol_role")
	if got, _ := c.RoleOID("pol_role"); got != oid {
		t.Fatalf("re-RegisterRole changed the OID: %d -> %d", oid, got)
	}

	// pg_roles exposes the registered role with its OID alongside the seeded
	// postgres row, sorted by name.
	pgRoles, found := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_roles"})
	if !found {
		t.Fatalf("pg_roles virtual table missing")
	}
	rows := pgRoles.VirtualRows()
	var sawPostgres, sawRole bool
	for _, r := range rows {
		switch r[1] {
		case "postgres":
			sawPostgres = true
		case "pol_role":
			sawRole = true
			if r[0] != strconv.FormatUint(uint64(oid), 10) {
				t.Fatalf("pg_roles pol_role oid = %s; want %d", r[0], oid)
			}
		}
	}
	if !sawPostgres || !sawRole {
		t.Fatalf("pg_roles rows missing postgres=%v pol_role=%v: %v", sawPostgres, sawRole, rows)
	}

	// DROP ROLE removes it from the registry.
	c.UnregisterRole("pol_role")
	if _, ok := c.RoleOID("pol_role"); ok {
		t.Fatalf("RoleOID(pol_role) still known after UnregisterRole")
	}
}
