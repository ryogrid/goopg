package executor

import (
    "testing"
    "strings"
    "github.com/goopg/goopg/internal/catalog"
)

// TestPlpgsqlCompositeFieldAccess verifies that PL/pgSQL composite type
// field access and assignment work correctly for the avg_state use case.
func TestPlpgsqlCompositeFieldAccess(t *testing.T) {
    cat := catalog.NewInMemory()
    
    // Register the composite type
    fields := []catalog.CompositeField{
        {Name: "total", ColType: "bigint"},
        {Name: "count", ColType: "bigint"},
    }
    cat.RegisterCompositeTypeWithFields("avg_state", fields)
    
    // Verify lookup works
    got := cat.LookupCompositeTypeFields("avg_state")
    if len(got) != 2 {
        t.Fatalf("expected 2 fields, got %d", len(got))
    }
    
    ctx := &Context{Catalog: cat}
    
    // Register avg_transfn routine manually
    avgTransfnBody := `
declare new_state avg_state;
begin
    if state is null then
        if n is not null then
            new_state.total := n;
            new_state.count := 1;
            return new_state;
        end if;
        return null;
    elsif n is not null then
        state.total := state.total + n;
        state.count := state.count + 1;
        return state;
    end if;
    return null;
end`
    
    r := &catalog.Routine{
        Name:       "avg_transfn",
        Schema:     "public",
        Language:   "plpgsql",
        Body:       avgTransfnBody,
        ReturnType: catalog.Type{Name: "avg_state"},
        ArgNames:   []string{"state", "n"},
        ArgTypes:   []catalog.Type{{Name: "avg_state"}, {Name: "int4"}},
    }
    
    // Test: avg_transfn(NULL, 1)
    result, err := executePLpgSQLRoutine(r, []Datum{NullDatum, NewIntDatum(1)}, ctx, 0)
    if err != nil {
        t.Fatalf("executePLpgSQLRoutine(NULL, 1) error: %v", err)
    }
    t.Logf("avg_transfn(NULL, 1) = %v (kind=%v, isNull=%v)", result.Format(), result.Kind, result.IsNull())
    
    if result.IsNull() {
        t.Error("Expected non-null result from avg_transfn(NULL, 1)")
    }
    
    if !strings.Contains(result.Format(), "1") {
        t.Errorf("Expected result to contain '1', got: %v", result.Format())
    }
}

// TestPlpgsqlCompositeFieldChained verifies avg_transfn called twice then avg_finalfn.
func TestPlpgsqlCompositeFieldChained(t *testing.T) {
    cat := catalog.NewInMemory()
    cat.RegisterCompositeTypeWithFields("avg_state", []catalog.CompositeField{
        {Name: "total", ColType: "bigint"},
        {Name: "count", ColType: "bigint"},
    })
    ctx := &Context{Catalog: cat}
    
    avgTransfnBody := `
declare new_state avg_state;
begin
    if state is null then
        if n is not null then
            new_state.total := n;
            new_state.count := 1;
            return new_state;
        end if;
        return null;
    elsif n is not null then
        state.total := state.total + n;
        state.count := state.count + 1;
        return state;
    end if;
    return null;
end`
    
    avgFinalFnBody := `
begin
    if state is null then
        return null;
    else
        return state.total / state.count;
    end if;
end`
    
    transfn := &catalog.Routine{
        Name: "avg_transfn", Schema: "public", Language: "plpgsql",
        Body: avgTransfnBody,
        ReturnType: catalog.Type{Name: "avg_state"},
        ArgNames: []string{"state", "n"},
        ArgTypes: []catalog.Type{{Name: "avg_state"}, {Name: "int4"}},
    }
    finalfn := &catalog.Routine{
        Name: "avg_finalfn", Schema: "public", Language: "plpgsql",
        Body: avgFinalFnBody,
        ReturnType: catalog.Type{Name: "int4"},
        ArgNames: []string{"state"},
        ArgTypes: []catalog.Type{{Name: "avg_state"}},
    }
    
    // avg_transfn(NULL, 1) → (1,1)
    s1, err := executePLpgSQLRoutine(transfn, []Datum{NullDatum, NewIntDatum(1)}, ctx, 0)
    if err != nil { t.Fatalf("step1 error: %v", err) }
    t.Logf("step1: %v", s1.Format())
    
    // avg_transfn((1,1), 3) → (4,2)
    s2, err := executePLpgSQLRoutine(transfn, []Datum{s1, NewIntDatum(3)}, ctx, 0)
    if err != nil { t.Fatalf("step2 error: %v", err) }
    t.Logf("step2: %v", s2.Format())
    
    // avg_finalfn((4,2)) → 2
    final, err := executePLpgSQLRoutine(finalfn, []Datum{s2}, ctx, 0)
    if err != nil { t.Fatalf("finalfn error: %v", err) }
    t.Logf("final: %v (kind=%v)", final.Format(), final.Kind)
    
    if final.IsNull() { t.Error("Expected non-null final result") }
    if final.Kind != KindInt || final.Int != 2 {
        t.Errorf("Expected final=2, got kind=%v val=%v", final.Kind, final.Format())
    }
}
