package catalog

import "testing"

// TestUserSchemasInDB pins the listing a running standby's catalog refresh
// uses to unregister schemas a replayed DROP SCHEMA removed (M0149-0032d):
// it returns the user schemas registered in one database, not the bootstrap
// schemas, and reflects an unregister.
func TestUserSchemasInDB(t *testing.T) {
	c := NewInMemory()
	c.RegisterSchemaDuringRecovery("drop_schema_test1", 16390, DefaultDBOid)
	got := c.UserSchemasInDB(DefaultDBOid)
	if got["drop_schema_test1"] != 16390 {
		t.Fatalf("UserSchemasInDB = %v, want drop_schema_test1=16390", got)
	}
	if _, ok := got["public"]; ok {
		t.Fatalf("bootstrap schema listed: %v", got)
	}
	c.UnregisterSchemaDuringRecovery("drop_schema_test1", DefaultDBOid)
	if _, ok := c.UserSchemasInDB(DefaultDBOid)["drop_schema_test1"]; ok {
		t.Fatal("unregistered schema still listed")
	}
}
