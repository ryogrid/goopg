package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestRegnamespaceAndRegprocInput pins the M0149-0001h1a13 reg* inputs:
// regnamespacein resolves a schema (3F000 on a miss, 42602 when qualified),
// regprocin/regprocedurein see the full pg_proc seed (regprocin needs a
// unique name, 42725), a pg_catalog-qualified seeded collation resolves, and
// to_regtypemod reports the typmod a type name declares.
func TestRegnamespaceAndRegprocInput(t *testing.T) {
	ctx := &Context{Catalog: catalog.NewInMemory()}
	in := func(s, typ string) (Datum, error) {
		return regIdentifierInput(NewStringDatum(s), typ, ctx, 0)
	}
	if d, err := in("pg_catalog", "regnamespace"); err != nil || d.Kind != KindInt || d.Int != 11 {
		t.Fatalf("pg_catalog = %+v, %v", d, err)
	}
	if _, err := in("nosuch", "regnamespace"); err == nil || err.(*ExecError).Code != "3F000" {
		t.Fatalf("nosuch: %v", err)
	}
	if _, err := in("a.b", "regnamespace"); err == nil || err.(*ExecError).Code != "42602" {
		t.Fatalf("a.b: %v", err)
	}
	if d, err := in("now", "regproc"); err != nil || d.Kind != KindInt || d.Int != 1299 {
		t.Fatalf("now = %+v, %v", d, err)
	}
	if _, err := in("abs", "regproc"); err == nil || err.(*ExecError).Code != "42725" {
		t.Fatalf("abs: %v", err)
	}
	if d, err := in("abs(numeric)", "regprocedure"); err != nil || d.Kind != KindInt || d.Int != 1705 {
		t.Fatalf("abs(numeric) = %+v, %v", d, err)
	}
	if d, err := in(`pg_catalog."POSIX"`, "regcollation"); err != nil || d.Int != 951 {
		t.Fatalf(`pg_catalog."POSIX" = %+v, %v`, d, err)
	}
	if d := toRegTypmod("varchar(32)", ctx); d.Int != 36 {
		t.Fatalf("varchar(32) = %+v", d)
	}
}
