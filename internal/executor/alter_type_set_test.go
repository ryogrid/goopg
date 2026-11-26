package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAlterTypeSetSendReceive: ALTER TYPE … SET (send = …, receive = …)
// records the base type's typsend / typreceive (AlterType, typecmds.c); NONE
// clears one, and an unknown property is 42601 "type attribute … not
// recognized".
func TestAlterTypeSetSendReceive(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	for _, sql := range []string{
		"CREATE TYPE myvarchar",
		"ALTER TYPE myvarchar SET (send = myvarcharsend, receive = myvarcharrecv)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	im := ctx.Catalog.(*catalog.InMemory)
	ct := im.LookupCompositeType("myvarchar", ctx.CurrentDatabaseOid)
	if ct == nil || ct.SendFunc != "myvarcharsend" || ct.ReceiveFunc != "myvarcharrecv" {
		t.Fatalf("type = %+v", ct)
	}
	if err := runDDL(t, ctx, "ALTER TYPE myvarchar SET (send = NONE)"); err != nil {
		t.Fatal(err)
	}
	if ct.SendFunc != "" {
		t.Errorf("send = NONE left %q", ct.SendFunc)
	}
	err := runDDL(t, ctx, "ALTER TYPE myvarchar SET (bogus = x)")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42601" {
		t.Errorf("unknown property: %v, want 42601", err)
	}
}
