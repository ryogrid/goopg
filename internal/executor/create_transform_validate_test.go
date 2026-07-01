package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestResolveTransformFunc exercises the PostgreSQL CreateTransform +
// check_transform_function rules (functioncmds.c) ported for CREATE
// TRANSFORM's FROM/TO SQL function resolution. DU-002 (M0119-0004).
func TestResolveTransformFunc(t *testing.T) {
	// register creates a routine named "f" in a fresh registry with the given
	// volatility/kind/returnset and (arg, return) types, then returns the
	// registry for resolveTransformFunc to look up "f" against.
	register := func(mutate func(*catalog.Routine)) *catalog.Routines {
		rs := catalog.NewRoutines()
		r := &catalog.Routine{
			Name:       "f",
			ArgTypes:   []catalog.Type{{Name: "internal"}},
			ReturnType: catalog.Type{Name: "internal"},
			Volatile:   "i",
		}
		if mutate != nil {
			mutate(r)
		}
		if _, err := rs.Create(r, false); err != nil {
			t.Fatalf("Routines.Create: %v", err)
		}
		return rs
	}
	fn := parser.ObjectName{Name: "f"}

	t.Run("from sql ok", func(t *testing.T) {
		rs := register(nil)
		oid, err := resolveTransformFunc(rs, fn, nil, true, "int")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if oid == 0 {
			t.Error("resolved OID = 0, want the routine's OID")
		}
	})

	t.Run("from sql wrong return type rejected", func(t *testing.T) {
		rs := register(func(r *catalog.Routine) { r.ReturnType = catalog.Type{Name: "integer"} })
		_, err := resolveTransformFunc(rs, fn, nil, true, "int")
		assertTransformErr(t, err, "return data type of FROM SQL function must be internal")
	})

	t.Run("to sql ok", func(t *testing.T) {
		rs := register(func(r *catalog.Routine) { r.ReturnType = catalog.Type{Name: "int4"} })
		oid, err := resolveTransformFunc(rs, fn, nil, false, "integer")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if oid == 0 {
			t.Error("resolved OID = 0, want the routine's OID")
		}
	})

	t.Run("to sql wrong return type rejected", func(t *testing.T) {
		rs := register(func(r *catalog.Routine) { r.ReturnType = catalog.Type{Name: "text"} })
		_, err := resolveTransformFunc(rs, fn, nil, false, "integer")
		assertTransformErr(t, err, "return data type of TO SQL function must be the transform data type")
	})

	t.Run("volatile rejected", func(t *testing.T) {
		rs := register(func(r *catalog.Routine) { r.Volatile = "v" })
		_, err := resolveTransformFunc(rs, fn, nil, true, "int")
		assertTransformErr(t, err, "transform function must not be volatile")
	})

	t.Run("procedure rejected", func(t *testing.T) {
		rs := register(func(r *catalog.Routine) { r.IsProcedure = true })
		_, err := resolveTransformFunc(rs, fn, nil, true, "int")
		assertTransformErr(t, err, "transform function must be a normal function")
	})

	t.Run("returns set rejected", func(t *testing.T) {
		rs := register(func(r *catalog.Routine) { r.ReturnsSet = true })
		_, err := resolveTransformFunc(rs, fn, nil, true, "int")
		assertTransformErr(t, err, "transform function must not return a set")
	})

	t.Run("wrong arg count rejected", func(t *testing.T) {
		rs := register(func(r *catalog.Routine) {
			r.ArgTypes = []catalog.Type{{Name: "internal"}, {Name: "internal"}}
		})
		_, err := resolveTransformFunc(rs, fn, nil, true, "int")
		assertTransformErr(t, err, "transform function must take one argument")
	})

	t.Run("wrong arg type rejected", func(t *testing.T) {
		rs := register(func(r *catalog.Routine) { r.ArgTypes = []catalog.Type{{Name: "text"}} })
		_, err := resolveTransformFunc(rs, fn, nil, true, "int")
		assertTransformErr(t, err, "first argument of transform function must be type internal")
	})

	t.Run("unresolved builtin is lenient", func(t *testing.T) {
		rs := catalog.NewRoutines() // empty — "not_a_real_builtin" isn't user-created or curated
		oid, err := resolveTransformFunc(rs, parser.ObjectName{Name: "not_a_real_builtin"}, []string{"internal"}, false, "int")
		if err != nil {
			t.Fatalf("unresolved builtin should not error, got: %v", err)
		}
		if oid != 0 {
			t.Errorf("unresolved builtin OID = %d, want 0", oid)
		}
	})

	t.Run("curated builtin int4recv resolves TO SQL", func(t *testing.T) {
		rs := catalog.NewRoutines() // empty — int4recv comes from catalog.LookupBuiltinProc
		oid, err := resolveTransformFunc(rs, parser.ObjectName{Name: "int4recv"}, []string{"internal"}, false, "int")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if oid != 2406 {
			t.Errorf("resolved OID = %d, want 2406 (int4recv)", oid)
		}
	})

	t.Run("curated builtin prsd_lextype resolves FROM SQL", func(t *testing.T) {
		rs := catalog.NewRoutines() // empty — prsd_lextype comes from catalog.LookupBuiltinProc
		oid, err := resolveTransformFunc(rs, parser.ObjectName{Name: "prsd_lextype"}, []string{"internal"}, true, "int")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if oid != 3721 {
			t.Errorf("resolved OID = %d, want 3721 (prsd_lextype)", oid)
		}
	})

	t.Run("curated builtin schema-qualified pg_catalog resolves", func(t *testing.T) {
		rs := catalog.NewRoutines()
		oid, err := resolveTransformFunc(rs, parser.ObjectName{Schema: "pg_catalog", Name: "int4recv"}, []string{"internal"}, false, "int")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if oid != 2406 {
			t.Errorf("resolved OID = %d, want 2406 (int4recv)", oid)
		}
	})

	t.Run("curated builtin other schema-qualified stays unresolved", func(t *testing.T) {
		rs := catalog.NewRoutines()
		oid, err := resolveTransformFunc(rs, parser.ObjectName{Schema: "public", Name: "int4recv"}, []string{"internal"}, false, "int")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if oid != 0 {
			t.Errorf("resolved OID = %d, want 0 (public.int4recv is not the builtin)", oid)
		}
	})

	t.Run("curated builtin wrong TO SQL return type rejected", func(t *testing.T) {
		rs := catalog.NewRoutines()
		_, err := resolveTransformFunc(rs, parser.ObjectName{Name: "int4recv"}, []string{"internal"}, false, "text")
		assertTransformErr(t, err, "return data type of TO SQL function must be the transform data type")
	})
}

func assertTransformErr(t *testing.T, err error, wantSubstr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", wantSubstr)
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error = %q, want substring %q", err.Error(), wantSubstr)
	}
}
