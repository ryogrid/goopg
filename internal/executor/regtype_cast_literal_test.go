package executor

// review/260831-2 X-7 — a `'name'::regtype` literal cast never became an OID.
//
// A reg* datum is a plain KindInt holding an object OID (the model documented
// at evalCastTyped's reg*→string guard), and the regclass arm of the same
// CastExpr switch has resolved names through the shared regclassin port since
// M0134-0168. The regtype arm resolved USER types that way but fell through for
// a built-in name and returned the raw string, so against the PG 18.3 oracle:
//
//	'int4'::regtype        PG "integer"   goopg "int4"
//	'int4'::regtype::oid   PG 23          goopg ERROR invalid input syntax for
//	                                            type oid: "int4"
//	'nosuchtype'::regtype  PG 42704       goopg "nosuchtype"
//
// The regtype COLUMN path (operators_storage.go) was already correct — only the
// literal-cast path bypassed regIdentifierInput.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func TestRegTypeLiteralCastResolvesBuiltinName(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	// The datum itself is the OID, so the cast chains on to oid/text exactly as
	// PG's regtypein → regtypeout pair does.
	rows := runQuery(t, ctx, `SELECT 'int4'::regtype, 'int4'::regtype::oid, 'int4'::regtype::text`)
	if len(rows) != 1 || len(rows[0]) != 3 {
		t.Fatalf("unexpected shape: %v", rows)
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != int64(catalog.OIDInt4) {
		t.Errorf("'int4'::regtype = %v (kind %d), want KindInt %d",
			rows[0][0], rows[0][0].Kind, catalog.OIDInt4)
	}
	if rows[0][1].Kind != KindInt || rows[0][1].Int != int64(catalog.OIDInt4) {
		t.Errorf("'int4'::regtype::oid = %v, want %d", rows[0][1], catalog.OIDInt4)
	}
	// regtypeout renders format_type_be's SQL spelling, not the catalog name.
	if got := rows[0][2].StringValue(); got != "integer" {
		t.Errorf("'int4'::regtype::text = %q, want %q", got, "integer")
	}

	// An unknown name is regtypein's 42704, not a pass-through string.
	if _, err := runQueryErr(t, ctx, `SELECT 'nosuchtype'::regtype`); err == nil {
		t.Error("'nosuchtype'::regtype should raise 42704")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("'nosuchtype'::regtype err = %v, want 42704", err)
	}

	// The schema qualifier the shared port honors (ES-8) reaches this path too.
	if _, err := runQueryErr(t, ctx, `SELECT 'nosuchschema.int4'::regtype`); err == nil {
		t.Error("'nosuchschema.int4'::regtype should raise 3F000")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "3F000" {
		t.Errorf("'nosuchschema.int4'::regtype err = %v, want 3F000", err)
	}
}

func TestRegTypeLiteralCastStillResolvesUserType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TYPE ct AS ENUM ('a')`); err != nil {
		t.Fatalf("CREATE TYPE ct: %v", err)
	}
	ctOID, ok := userTypeOIDForName(ctx.Catalog, "ct")
	if !ok {
		t.Fatal("ct not registered")
	}
	rows := runQuery(t, ctx, `SELECT 'ct'::regtype, 'ct'::regtype::text`)
	if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != int64(ctOID) {
		t.Fatalf("'ct'::regtype = %v, want KindInt %d", rows, ctOID)
	}
	if got := rows[0][1].StringValue(); got != "ct" {
		t.Errorf("'ct'::regtype::text = %q, want %q", got, "ct")
	}
}
