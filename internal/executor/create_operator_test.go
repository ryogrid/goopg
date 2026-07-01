package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCreateOperatorCommutatorNegatorBackPatch verifies the two-pass
// forward-reference resolution (mirroring PG's get_other_operator/
// OperatorShellMake/OperatorUpd, pg_operator.c): a CREATE OPERATOR whose
// NEGATOR names an operator that does not exist yet forward-declares a
// shell (FuncOID==0, excluded from pg_dump output) purely to mint a stable
// OID, and back-patches that shell's own NegatorOID to point back — so a
// later CREATE OPERATOR statement defining the referenced operator (which
// reuses the shell's OID, since RegisterUserOperator is idempotent by key)
// comes out already linked in both directions despite only one statement
// declaring NEGATOR explicitly. DU-002 slice 407.
func TestCreateOperatorCommutatorNegatorBackPatch(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()

	if err := runDDL(t, ctx, `CREATE OPERATOR public.=== (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4, NEGATOR = public.!==)`); err != nil {
		t.Fatalf("first CREATE OPERATOR: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)

	eqOp, ok := im.LookupUserOperator("public", "===", "int4", "int4")
	if !ok {
		t.Fatal("=== not registered")
	}
	shell, ok := im.LookupUserOperator("public", "!==", "int4", "int4")
	if !ok {
		t.Fatal("!== shell not registered")
	}
	if shell.FuncOID != 0 {
		t.Errorf("!== shell FuncOID = %d, want 0 (unfilled shell)", shell.FuncOID)
	}
	if eqOp.NegatorOID != shell.OID {
		t.Errorf("===.NegatorOID = %d, want shell OID %d", eqOp.NegatorOID, shell.OID)
	}
	if shell.NegatorOID != eqOp.OID {
		t.Errorf("!== shell.NegatorOID = %d, want back-patched to ===.OID %d", shell.NegatorOID, eqOp.OID)
	}

	// A pg_operator.VirtualRows call before the shell is filled in must skip
	// it entirely (mirrors real PG's dumpOpr skipping an invalid oprcode).
	rows := pgOperatorVirtualRows(t, im)
	if len(rows) != 1 {
		t.Fatalf("VirtualRows before fill-in = %d rows, want 1 (shell excluded): %v", len(rows), rows)
	}

	if err := runDDL(t, ctx, `CREATE OPERATOR public.!== (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("second CREATE OPERATOR: %v", err)
	}
	neOp, ok := im.LookupUserOperator("public", "!==", "int4", "int4")
	if !ok {
		t.Fatal("!== not registered after fill-in")
	}
	if neOp.OID != shell.OID {
		t.Errorf("!== OID changed on fill-in: got %d, want reused shell OID %d", neOp.OID, shell.OID)
	}
	if neOp.FuncOID == 0 {
		t.Error("!== FuncOID still 0 after fill-in")
	}
	if neOp.NegatorOID != eqOp.OID {
		t.Errorf("!==.NegatorOID = %d, want %d (preserved across fill-in)", neOp.NegatorOID, eqOp.OID)
	}

	rows = pgOperatorVirtualRows(t, im)
	if len(rows) != 2 {
		t.Fatalf("VirtualRows after fill-in = %d rows, want 2: %v", len(rows), rows)
	}
}

// TestCreateOperatorSelfCommutator verifies a symmetric operator declaring
// itself as its own COMMUTATOR (e.g. "=") resolves without creating a
// shell, mirroring PG's selfCommutator handling in OperatorCreate.
func TestCreateOperatorSelfCommutator(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR public.=== (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4, COMMUTATOR = public.===)`); err != nil {
		t.Fatalf("CREATE OPERATOR: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	op, ok := im.LookupUserOperator("public", "===", "int4", "int4")
	if !ok {
		t.Fatal("=== not registered")
	}
	if op.CommutatorOID != op.OID {
		t.Errorf("CommutatorOID = %d, want self OID %d", op.CommutatorOID, op.OID)
	}
}

// TestCreateOperatorSelfNegatorRejected verifies PG's "operator cannot be
// its own negator" rejection (OperatorCreate, pg_operator.c) — unlike
// self-commutator, self-negation is nonsensical and always an error.
func TestCreateOperatorSelfNegatorRejected(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	err := runDDL(t, ctx, `CREATE OPERATOR public.=== (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4, NEGATOR = public.===)`)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be its own negator") {
		t.Errorf("error = %v, want mention of 'cannot be its own negator'", err)
	}
}

// TestCreateOperatorUnaryAndValidation exercises the unary (prefix) operator
// form (LEFTARG omitted, RIGHTARG required) and OperatorValidateParams-style
// attribute checks (operatorcmds.c): COMMUTATOR requires a binary operator;
// HASHES/MERGES require a boolean-returning function.
func TestCreateOperatorUnaryAndValidation(t *testing.T) {
	newCtx := func() *Context {
		c := NewContext()
		c.Catalog = catalog.NewInMemory()
		return c
	}

	t.Run("unary ok", func(t *testing.T) {
		ctx := newCtx()
		if err := runDDL(t, ctx, `CREATE OPERATOR public.@-@ (FUNCTION = int4eq, RIGHTARG = int4)`); err != nil {
			t.Fatalf("unary CREATE OPERATOR: %v", err)
		}
		im := ctx.Catalog.(*catalog.InMemory)
		op, ok := im.LookupUserOperator("public", "@-@", "", "int4")
		if !ok {
			t.Fatal("@-@ not registered")
		}
		rows := pgOperatorVirtualRows(t, im)
		found := false
		for _, r := range rows {
			if r[1] == "@-@" {
				found = true
				if r[4] != "l" {
					t.Errorf("oprkind = %q, want %q (unary/prefix)", r[4], "l")
				}
				if r[7] != "0" {
					t.Errorf("oprleft = %q, want \"0\" (no left arg)", r[7])
				}
			}
		}
		if !found {
			t.Fatal("@-@ row missing from VirtualRows")
		}
		_ = op
	})

	t.Run("postfix rejected", func(t *testing.T) {
		ctx := newCtx()
		err := runDDL(t, ctx, `CREATE OPERATOR public.@! (FUNCTION = int4eq, LEFTARG = int4)`)
		if err == nil || !strings.Contains(err.Error(), "right argument type must be specified") {
			t.Fatalf("error = %v, want postfix-rejection message", err)
		}
	})

	t.Run("commutator on unary rejected", func(t *testing.T) {
		ctx := newCtx()
		err := runDDL(t, ctx, `CREATE OPERATOR public.@-@ (FUNCTION = int4eq, RIGHTARG = int4, COMMUTATOR = public.@-@)`)
		if err == nil || !strings.Contains(err.Error(), "only binary operators can have commutators") {
			t.Fatalf("error = %v, want binary-only commutator message", err)
		}
	})

	t.Run("hashes on non-boolean rejected", func(t *testing.T) {
		ctx := newCtx()
		err := runDDL(t, ctx, `CREATE OPERATOR public.%%% (FUNCTION = int4recv, LEFTARG = int4, RIGHTARG = int4, HASHES)`)
		if err == nil || !strings.Contains(err.Error(), "only boolean operators can hash") {
			t.Fatalf("error = %v, want boolean-only hash message", err)
		}
	})

	t.Run("merges and hashes on boolean binary op accepted", func(t *testing.T) {
		ctx := newCtx()
		if err := runDDL(t, ctx, `CREATE OPERATOR public.~=~ (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4, MERGES, HASHES)`); err != nil {
			t.Fatalf("CREATE OPERATOR: %v", err)
		}
		im := ctx.Catalog.(*catalog.InMemory)
		op, ok := im.LookupUserOperator("public", "~=~", "int4", "int4")
		if !ok {
			t.Fatal("~=~ not registered")
		}
		if !op.CanMerge || !op.CanHash {
			t.Errorf("CanMerge=%v CanHash=%v, want both true", op.CanMerge, op.CanHash)
		}
	})
}

// pgOperatorVirtualRows invokes pg_operator's VirtualRows function through
// the catalog table registry, mirroring how pg_dump's getOperators reads it.
func pgOperatorVirtualRows(t *testing.T, im *catalog.InMemory) [][]string {
	t.Helper()
	tbl, ok := im.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_operator"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_catalog.pg_operator virtual table/VirtualRows not found")
	}
	return tbl.VirtualRows()
}
