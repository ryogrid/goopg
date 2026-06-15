package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// TestEvalAclDefault pins acldefault("char", oid) to PostgreSQL's hard-wired
// defaults (src/backend/utils/adt/acl.c acldefault/acldefault_sql). pg_dump's
// getNamespaces issues `acldefault('n', n.nspowner)` and diffs the result
// against nspacl to decide whether to emit GRANT/REVOKE; the text form must
// match upstream exactly (owner OID 10 → "postgres", privilege letters in
// ACL_ALL_RIGHTS_STR order). M0110-0001 / DU-002 slice 2.
func TestEvalAclDefault(t *testing.T) {
	cases := []struct {
		name    string
		objtype string
		owner   int64
		want    string
	}{
		// Schema: owner gets USAGE|CREATE, no PUBLIC entry. This is the exact
		// shape pg_dump's getNamespaces consumes.
		{"schema", "n", 10, "{postgres=UC/postgres}"},
		// Table: owner gets all relation rights in ACL_ALL_RIGHTS_STR order.
		{"table", "r", 10, "{postgres=arwdDxtm/postgres}"},
		// Sequence: USAGE|SELECT|UPDATE → "rwU".
		{"sequence", "s", 10, "{postgres=rwU/postgres}"},
		// Database: PUBLIC gets TEMP|CONNECT, owner gets CREATE|TEMP|CONNECT.
		{"database", "d", 10, "{=Tc/postgres,postgres=CTc/postgres}"},
		// Function: PUBLIC and owner both get EXECUTE.
		{"function", "f", 10, "{=X/postgres,postgres=X/postgres}"},
		// Language: PUBLIC and owner both get USAGE.
		{"language", "l", 10, "{=U/postgres,postgres=U/postgres}"},
		// Type/domain: PUBLIC and owner both get USAGE.
		{"type", "T", 10, "{=U/postgres,postgres=U/postgres}"},
		// Large object: owner gets SELECT|UPDATE → "rw".
		{"largeobject", "L", 10, "{postgres=rw/postgres}"},
		// Tablespace / FDW / foreign server: single owner privilege.
		{"tablespace", "t", 10, "{postgres=C/postgres}"},
		{"fdw", "F", 10, "{postgres=U/postgres}"},
		{"foreign server", "S", 10, "{postgres=U/postgres}"},
		// Parameter ACL: owner gets SET|ALTER SYSTEM → "sA".
		{"parameter acl", "p", 10, "{postgres=sA/postgres}"},
		// Column: no default privileges → empty array.
		{"column", "c", 10, "{}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &planner.FuncCall{
				Name: "acldefault",
				Args: []planner.Expr{
					&planner.StringConst{Value: c.objtype},
					&planner.IntegerConst{Value: c.owner},
				},
			}
			got, err := evalFuncCall(fc, nil, &Context{})
			if err != nil {
				t.Fatalf("evalFuncCall: %v", err)
			}
			if got.Kind != KindString || got.StringValue() != c.want {
				t.Fatalf("acldefault(%q, %d) = %+v, want %q", c.objtype, c.owner, got, c.want)
			}
		})
	}
}

// TestEvalAclDefaultEdgeCases covers NULL propagation and the unrecognized
// object-type error (PG raises elog ERROR "unrecognized object type
// abbreviation").
func TestEvalAclDefaultEdgeCases(t *testing.T) {
	t.Run("NULL objtype propagates", func(t *testing.T) {
		fc := &planner.FuncCall{
			Name: "acldefault",
			Args: []planner.Expr{&planner.NullConst{}, &planner.IntegerConst{Value: 10}},
		}
		got, err := evalFuncCall(fc, nil, &Context{})
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if !got.IsNull() {
			t.Fatalf("got %+v, want NULL", got)
		}
	})
	t.Run("NULL owner propagates", func(t *testing.T) {
		fc := &planner.FuncCall{
			Name: "acldefault",
			Args: []planner.Expr{&planner.StringConst{Value: "n"}, &planner.NullConst{}},
		}
		got, err := evalFuncCall(fc, nil, &Context{})
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if !got.IsNull() {
			t.Fatalf("got %+v, want NULL", got)
		}
	})
	t.Run("unrecognized objtype errors", func(t *testing.T) {
		fc := &planner.FuncCall{
			Name: "acldefault",
			Args: []planner.Expr{&planner.StringConst{Value: "z"}, &planner.IntegerConst{Value: 10}},
		}
		if _, err := evalFuncCall(fc, nil, &Context{}); err == nil {
			t.Fatalf("expected error for unrecognized object type, got nil")
		}
	})
}
