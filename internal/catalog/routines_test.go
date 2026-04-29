package catalog

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func makeRoutine(schema, name string, args []Type, ret Type, body string) *Routine {
	return &Routine{
		Schema:     schema,
		Name:       name,
		ArgTypes:   args,
		ReturnType: ret,
		Language:   "plpgsql",
		Body:       body,
	}
}

// TestRoutinesCreateAssignsOID pins the OID-assignment contract:
// each new routine gets a fresh OID from FirstRoutineOID upward.
func TestRoutinesCreateAssignsOID(t *testing.T) {
	rs := NewRoutines()
	r1, err := rs.Create(makeRoutine("public", "f1", nil, Type{Name: "int"}, "BEGIN END"), false)
	if err != nil {
		t.Fatal(err)
	}
	if r1.OID < FirstRoutineOID {
		t.Errorf("OID %d below FirstRoutineOID %d", r1.OID, FirstRoutineOID)
	}
	r2, err := rs.Create(makeRoutine("public", "f2", nil, Type{Name: "int"}, "BEGIN END"), false)
	if err != nil {
		t.Fatal(err)
	}
	if r2.OID <= r1.OID {
		t.Errorf("OIDs not monotonic: r1=%d r2=%d", r1.OID, r2.OID)
	}
}

// TestRoutinesCreateRejectsDuplicate guards the no-OR-REPLACE
// contract: a second Create with the same signature returns
// ErrRoutineExists.
func TestRoutinesCreateRejectsDuplicate(t *testing.T) {
	rs := NewRoutines()
	if _, err := rs.Create(makeRoutine("public", "f", []Type{{Name: "int"}}, Type{Name: "int"}, "v1"), false); err != nil {
		t.Fatal(err)
	}
	_, err := rs.Create(makeRoutine("public", "f", []Type{{Name: "int"}}, Type{Name: "int"}, "v2"), false)
	if !errors.Is(err, ErrRoutineExists) {
		t.Fatalf("got err=%v, want ErrRoutineExists", err)
	}
}

// TestRoutinesCreateOrReplacePreservesOID pins upstream's contract:
// CREATE OR REPLACE keeps the existing OID so any references
// (e.g. function-default expressions, view definitions) stay valid
// across the redefinition.
func TestRoutinesCreateOrReplacePreservesOID(t *testing.T) {
	rs := NewRoutines()
	v1, err := rs.Create(makeRoutine("public", "f", nil, Type{Name: "int"}, "v1"), false)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := rs.Create(makeRoutine("public", "f", nil, Type{Name: "int"}, "v2"), true)
	if err != nil {
		t.Fatal(err)
	}
	if v2.OID != v1.OID {
		t.Errorf("OID changed across OR REPLACE: %d -> %d", v1.OID, v2.OID)
	}
	if v2.Body != "v2" {
		t.Errorf("Body = %q, want v2", v2.Body)
	}
}

// TestRoutinesOverloadedNamesDistinct pins overload semantics:
// same schema+name with different argtypes is two separate
// registrations.
func TestRoutinesOverloadedNamesDistinct(t *testing.T) {
	rs := NewRoutines()
	rInt, err := rs.Create(makeRoutine("public", "f", []Type{{Name: "int"}}, Type{Name: "int"}, "int-body"), false)
	if err != nil {
		t.Fatal(err)
	}
	rText, err := rs.Create(makeRoutine("public", "f", []Type{{Name: "text"}}, Type{Name: "int"}, "text-body"), false)
	if err != nil {
		t.Fatal(err)
	}
	if rInt.OID == rText.OID {
		t.Errorf("overloads share OID: %d", rInt.OID)
	}
	got, ok := rs.Lookup(parser.ObjectName{Schema: "public", Name: "f"}, []Type{{Name: "int"}})
	if !ok || got.OID != rInt.OID {
		t.Errorf("lookup int → %v %v, want rInt OID %d", got, ok, rInt.OID)
	}
	got, ok = rs.Lookup(parser.ObjectName{Schema: "public", Name: "f"}, []Type{{Name: "text"}})
	if !ok || got.OID != rText.OID {
		t.Errorf("lookup text → %v %v, want rText OID %d", got, ok, rText.OID)
	}
	if all := rs.LookupByName(parser.ObjectName{Schema: "public", Name: "f"}); len(all) != 2 {
		t.Errorf("LookupByName found %d routines, want 2", len(all))
	}
}

// TestRoutinesDropRemovesEntry pins the standard drop path: the
// signature lookup stops resolving and the byName index is updated.
func TestRoutinesDropRemovesEntry(t *testing.T) {
	rs := NewRoutines()
	if _, err := rs.Create(makeRoutine("public", "f", nil, Type{Name: "int"}, "v"), false); err != nil {
		t.Fatal(err)
	}
	name := parser.ObjectName{Schema: "public", Name: "f"}
	if err := rs.Drop(name, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := rs.Lookup(name, nil); ok {
		t.Errorf("Lookup after Drop must miss")
	}
	if all := rs.LookupByName(name); len(all) != 0 {
		t.Errorf("LookupByName after Drop returned %d", len(all))
	}
	// Re-Drop surfaces ErrRoutineNotFound.
	if err := rs.Drop(name, nil); !errors.Is(err, ErrRoutineNotFound) {
		t.Errorf("re-Drop err=%v, want ErrRoutineNotFound", err)
	}
}

// TestRoutinesDropByNameAmbiguous pins the upstream "function name
// is not unique" contract: bare-name DROP must fail when more
// than one overload exists.
func TestRoutinesDropByNameAmbiguous(t *testing.T) {
	rs := NewRoutines()
	if _, err := rs.Create(makeRoutine("public", "f", []Type{{Name: "int"}}, Type{Name: "int"}, "i"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Create(makeRoutine("public", "f", []Type{{Name: "text"}}, Type{Name: "int"}, "t"), false); err != nil {
		t.Fatal(err)
	}
	err := rs.DropByName(parser.ObjectName{Schema: "public", Name: "f"})
	if !errors.Is(err, ErrRoutineAmbiguous) {
		t.Errorf("got err=%v, want ErrRoutineAmbiguous", err)
	}
}

// TestRoutinesDefaultSchemaIsPublic guards a usability rule —
// callers omitting the schema land routines in `public`, matching
// upstream's default search_path[0].
func TestRoutinesDefaultSchemaIsPublic(t *testing.T) {
	rs := NewRoutines()
	r, err := rs.Create(makeRoutine("", "f", nil, Type{Name: "int"}, "v"), false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Schema != "public" {
		t.Errorf("Schema = %q, want public", r.Schema)
	}
	// Lookup with empty schema must resolve.
	if _, ok := rs.Lookup(parser.ObjectName{Name: "f"}, nil); !ok {
		t.Errorf("Lookup with empty schema must default to public")
	}
}

// TestRoutinesListIsOIDOrdered pins the ordering contract the
// `pg_proc` virtual view depends on.
func TestRoutinesListIsOIDOrdered(t *testing.T) {
	rs := NewRoutines()
	for _, n := range []string{"a", "c", "b"} {
		if _, err := rs.Create(makeRoutine("public", n, nil, Type{Name: "int"}, "v"), false); err != nil {
			t.Fatal(err)
		}
	}
	list := rs.List()
	if len(list) != 3 {
		t.Fatalf("List len = %d, want 3", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i].OID < list[i-1].OID {
			t.Errorf("List not OID-ordered at %d: %d < %d", i, list[i].OID, list[i-1].OID)
		}
	}
}
