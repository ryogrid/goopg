package executor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// TestEverySysBtreeInsertPathIndexHasSplitKeyMeta pins the two halves of the
// runtime system-btree machinery together: any index OID handed to
// insertCanonicalSysBtreeLeaf must also be registered in keyMetaForSysBtree.
//
// Why this has to be a SOURCE pin rather than a value test. The insert path
// never consults keyMetaForSysBtree — it only appends to a leaf-root page.
// The registry is read exclusively by the split (sys_catalog_btree_split.go)
// and descent (sys_catalog_btree_multilevel.go) paths, which run only once a
// leaf-root has actually filled. So an unregistered index behaves perfectly
// for its first page of entries and then fails with "split: unsupported
// system btree OID N" — a fault no unit test that merely inserts a few rows
// can reach, and no assertion over the registry alone can see, because
// nothing in the registry names the insert call sites.
//
// That is exactly how M-NIGHTLY AI-20260811-014635-002 escaped: nine indexes
// (pg_index ×2, pg_attrdef ×2, pg_rewrite ×2, pg_sequence, pg_extension ×2)
// were added to the insert path over several slices without a registry entry,
// and receipt-report.spec only tripped 2678 at permutation 152.
func TestEverySysBtreeInsertPathIndexHasSplitKeyMeta(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	pkg, ok := pkgs["executor"]
	if !ok {
		t.Fatalf("package executor not found in parsed dir")
	}

	// Collect const name -> integer value for every literal const in the
	// package, so the OID identifiers at the call sites can be resolved.
	consts := map[string]uint32{}
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.INT {
						continue
					}
					v, err := strconv.ParseUint(lit.Value, 0, 32)
					if err != nil {
						continue
					}
					consts[name.Name] = uint32(v)
				}
			}
		}
	}

	// Find every insertCanonicalSysBtreeLeaf call and check its index-OID
	// argument (the second one) against the registry.
	seen := 0
	for _, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || fn.Name != "insertCanonicalSysBtreeLeaf" {
				return true
			}
			if len(call.Args) < 2 {
				return true
			}
			ident, ok := call.Args[1].(*ast.Ident)
			if !ok {
				// A non-identifier OID argument would silently escape this
				// guard, so fail rather than skip.
				t.Errorf("%s: insertCanonicalSysBtreeLeaf index-OID argument is not a plain "+
					"identifier; this guard can no longer see it — extend the guard",
					fset.Position(call.Pos()))
				return true
			}
			oid, ok := consts[ident.Name]
			if !ok {
				t.Errorf("%s: cannot resolve index-OID const %q to a literal value — "+
					"extend the guard", fset.Position(call.Pos()), ident.Name)
				return true
			}
			seen++
			if _, registered := keyMetaForSysBtree(oid); !registered {
				t.Errorf("%s: %s (OID %d) is on the runtime insert path but has no "+
					"keyMetaForSysBtree entry; it will work until its leaf-root fills and "+
					"then fail the split with \"unsupported system btree OID %d\"",
					fset.Position(call.Pos()), ident.Name, oid, oid)
			}
			return true
		})
	}

	// Non-vacuity: if the call-site scan silently found nothing (renamed
	// helper, moved file), the loop above would pass while checking zero
	// indexes.
	if seen < 50 {
		t.Fatalf("only %d insertCanonicalSysBtreeLeaf call sites resolved; the scan is "+
			"probably broken rather than the code being small", seen)
	}
}
