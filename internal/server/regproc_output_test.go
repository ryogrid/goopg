package server

import (
	"io"
	"log/slog"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// TestAppendTypedCellTextRegprocRendersName pins the M0119-0004 regproc
// output-formatting gap (loop #61/#62 deferral): a direct SELECT of a
// regproc/regprocedure-typed column (pg_type.typinput/typoutput,
// pg_operator.oprcode/oprrest/oprjoin, pg_am.amproc) stores the raw pg_proc
// OID and previously fell through appendTypedCellText's default case
// (AppendValueText), rendering the bare number instead of PG's regprocout
// function-name text. InvalidOid (0) renders "-", mirroring the existing
// `::regproc` CastExpr special-case (executor/expr.go) and the regclass
// case in this same switch. regprocedure additionally renders the
// argument-type list (format_procedure/regprocedureout), added in a later
// loop — see catalog.RegprocedureName.
func TestAppendTypedCellTextRegprocRendersName(t *testing.T) {
	cat := catalog.NewInMemory()
	userFunc, err := cat.Routines().Create(&catalog.Routine{
		Name:       "my_udf",
		Schema:     "public",
		ReturnType: catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create: %v", err)
	}

	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: cat,
	})

	regType := catalog.Type{Name: "regproc"}
	tests := []struct {
		name string
		oid  int64
		want string
	}{
		{"invalid oid renders dash", 0, "-"},
		{"built-in int4out resolves by name", 43, "int4out"},
		{"built-in int4in resolves by name", 42, "int4in"},
		{"user-defined function resolves via routine registry", int64(userFunc.OID), "my_udf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(srv.appendTypedCellText(nil, executor.NewIntDatum(tt.oid), regType))
			if got != tt.want {
				t.Errorf("appendTypedCellText(oid=%d, regproc) = %q, want %q", tt.oid, got, tt.want)
			}
		})
	}

	// regprocedure additionally renders the INPUT argument-type list
	// (format_procedure/regprocedureout): int4out(int4) -> "int4out(integer)".
	regProcedureType := catalog.Type{Name: "regprocedure"}
	if got := string(srv.appendTypedCellText(nil, executor.NewIntDatum(43), regProcedureType)); got != "int4out(integer)" {
		t.Errorf("appendTypedCellText(oid=43, regprocedure) = %q, want %q", got, "int4out(integer)")
	}

	// An OID unresolvable by either source falls back to the raw numeric
	// text (defensive; every OID a real BKI column references is present
	// in the generated index, mirroring aggBuiltinFuncName's fallback).
	if got := string(srv.appendTypedCellText(nil, executor.NewIntDatum(999999999), regType)); got != "999999999" {
		t.Errorf("appendTypedCellText(unresolvable oid) = %q, want %q", got, "999999999")
	}
}
