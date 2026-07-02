package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCommentOnCastResolvesTypeNameSynonym guards the loop #51 deferral: the
// DROP CAST type-name-synonym key fix (catalog.castKey/castKeyTypeName)
// resolves each side through TypeNameToOID before keying, so DROP CAST/
// CastByTypes find a cast under any PG built-in type-name synonym (e.g.
// "real" for "float4"). COMMENT ON CAST's executor handler
// (operators_ddl.go, `case "cast"`) calls the same catalog.CastByTypes choke
// point, so it should inherit the fix automatically — this test verifies
// that independently rather than relying on inference from the CastByTypes
// unit tests in internal/catalog/cast_synonym_test.go.
func TestCommentOnCastResolvesTypeNameSynonym(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE CAST (float4 AS text) WITHOUT FUNCTION`); err != nil {
		t.Fatalf("CREATE CAST (float4 AS text): %v", err)
	}
	// "real" is PG's built-in synonym for float4; a real pg_dump cluster
	// resolves both names to the same pg_type OID.
	if err := runDDL(t, ctx, `COMMENT ON CAST (real AS text) IS 'synonym-spelled cast comment'`); err != nil {
		t.Fatalf("COMMENT ON CAST (real AS text): %v", err)
	}

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	cst := im.CastByTypes("float4", "text")
	if cst == nil {
		t.Fatal("CastByTypes(\"float4\", \"text\") = nil after CREATE CAST")
	}

	const oidPgCast = 2605
	var found bool
	for _, cmt := range im.AllComments() {
		if cmt.ClassOID == oidPgCast && cmt.ObjOID == cst.OID && cmt.Description == "synonym-spelled cast comment" {
			found = true
		}
	}
	if !found {
		t.Fatalf("COMMENT ON CAST via synonym not stored under pg_cast oid=%d; AllComments=%+v", cst.OID, im.AllComments())
	}
}
