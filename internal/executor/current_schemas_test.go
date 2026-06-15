package executor

import "testing"

// current_schemas(boolean) must return a parseable `{a,b}` text array literal
// (name[]), not a bare scalar — pg_dump's parsePGArray rejects a scalar and
// aborts with "could not parse result of current_schemas()". current_schema
// stays scalar. (M0110-0001 DU-002 slice 33.)
func TestCurrentSchemasArrayLiteral(t *testing.T) {
	ctx := &Context{} // nil GetSetting → default search_path "$user", public

	// current_schema is the scalar first existing search-path schema.
	scalar, err := currentSchemaFromSearchPath(ctx)
	if err != nil {
		t.Fatalf("currentSchemaFromSearchPath: %v", err)
	}
	if got := scalar.StringValue(); got != "public" {
		t.Errorf("current_schema = %q, want %q", got, "public")
	}

	// current_schemas(false): explicit search-path schemas only.
	noImplicit, err := currentSchemasArray(ctx, false)
	if err != nil {
		t.Fatalf("currentSchemasArray(false): %v", err)
	}
	if got := noImplicit.StringValue(); got != "{public}" {
		t.Errorf("current_schemas(false) = %q, want %q", got, "{public}")
	}

	// current_schemas(true): implicit pg_catalog prepended.
	withImplicit, err := currentSchemasArray(ctx, true)
	if err != nil {
		t.Fatalf("currentSchemasArray(true): %v", err)
	}
	if got := withImplicit.StringValue(); got != "{pg_catalog,public}" {
		t.Errorf("current_schemas(true) = %q, want %q", got, "{pg_catalog,public}")
	}
}
