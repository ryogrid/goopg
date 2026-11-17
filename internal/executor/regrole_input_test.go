package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestRegroleInput pins regrolein through the shared reg* port: a role name
// resolves to its OID, a miss is 42704, a qualified name is 42602, and '-' is
// InvalidOid — the cast used to hand back the input text unchanged.
func TestRegroleInput(t *testing.T) {
	ctx := &Context{Catalog: catalog.NewInMemory()}
	if d, err := regIdentifierInput(NewStringDatum("postgres"), "regrole", ctx, 0); err != nil || d.Kind != KindInt || d.Int != 10 {
		t.Fatalf("postgres = %+v, %v", d, err)
	}
	if d, err := regIdentifierInput(NewStringDatum(`"postgres"`), "regrole", ctx, 0); err != nil || d.Int != 10 {
		t.Fatalf(`"postgres" = %+v, %v`, d, err)
	}
	if d, err := regIdentifierInput(NewStringDatum("-"), "regrole", ctx, 0); err != nil || d.Int != 0 {
		t.Fatalf("- = %+v, %v", d, err)
	}
	if _, err := regIdentifierInput(NewStringDatum("nosuch"), "regrole", ctx, 0); err == nil || err.(*ExecError).Code != "42704" {
		t.Fatalf("nosuch: %v", err)
	}
	if _, err := regIdentifierInput(NewStringDatum("a.b"), "regrole", ctx, 0); err == nil || err.(*ExecError).Code != "42602" {
		t.Fatalf("a.b: %v", err)
	}
}
