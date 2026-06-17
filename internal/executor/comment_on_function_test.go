package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCommentOnFunctionStoresPgProcDescription verifies COMMENT ON FUNCTION
// resolves the routine by its argument signature and stores the description in
// pg_description keyed under pg_proc (classoid 1255). DU-002 slice 147.
func TestCommentOnFunctionStoresPgProcDescription(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION add_one(integer) RETURNS integer LANGUAGE sql AS $$ SELECT $1 + 1 $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `COMMENT ON FUNCTION add_one(integer) IS 'a function comment'`); err != nil {
		t.Fatalf("COMMENT ON FUNCTION: %v", err)
	}

	const oidPgProc = 1255
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	all := im.AllComments()
	var found bool
	for _, cmt := range all {
		if cmt.ClassOID == oidPgProc && cmt.Description == "a function comment" {
			found = true
		}
	}
	if !found {
		t.Fatalf("COMMENT ON FUNCTION not stored under pg_proc; AllComments=%+v", all)
	}
}
