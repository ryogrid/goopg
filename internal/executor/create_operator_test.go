package executor

import (
	"strconv"
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

// TestAlterOperatorSetRestrictJoin verifies RESTRICT/JOIN can be changed
// freely after CREATE (unlike COMMUTATOR/NEGATOR/MERGES/HASHES), including
// clearing via `= NONE`, mirroring AlterOperator (operatorcmds.c).
func TestAlterOperatorSetRestrictJoin(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR public.=== (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	op, ok := im.LookupUserOperator("public", "===", "int4", "int4")
	if !ok {
		t.Fatal("=== not registered")
	}
	if op.RestrictOID != 0 || op.JoinOID != 0 {
		t.Fatalf("expected unset RESTRICT/JOIN before ALTER, got %d/%d", op.RestrictOID, op.JoinOID)
	}

	if err := runDDL(t, ctx, `ALTER OPERATOR public.=== (int4, int4) SET (RESTRICT = eqsel, JOIN = eqjoinsel)`); err != nil {
		t.Fatalf("ALTER OPERATOR SET: %v", err)
	}
	if op.RestrictOID == 0 || op.JoinOID == 0 {
		t.Fatalf("expected RESTRICT/JOIN set after ALTER, got %d/%d", op.RestrictOID, op.JoinOID)
	}

	// Changing to a different estimator is allowed (unlike commutator/negator).
	if err := runDDL(t, ctx, `ALTER OPERATOR public.=== (int4, int4) SET (JOIN = neqjoinsel)`); err != nil {
		t.Fatalf("ALTER OPERATOR SET (change join): %v", err)
	}

	// Clearing via NONE.
	if err := runDDL(t, ctx, `ALTER OPERATOR public.=== (int4, int4) SET (RESTRICT = NONE)`); err != nil {
		t.Fatalf("ALTER OPERATOR SET (clear restrict): %v", err)
	}
	if op.RestrictOID != 0 {
		t.Errorf("RestrictOID = %d after SET NONE, want 0", op.RestrictOID)
	}
}

// TestAlterOperatorSetCommutatorNegatorOnceOnly verifies COMMUTATOR/NEGATOR
// can be set once via ALTER (with back-patching, mirroring CREATE OPERATOR),
// a no-op restatement of the same value is allowed, but changing to a
// different value is rejected — matching AlterOperator's "cannot be changed
// if it has already been set" behaviour.
func TestAlterOperatorSetCommutatorNegatorOnceOnly(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR public.=== (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR ===: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE OPERATOR public.!== (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR !==: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	eqOp, _ := im.LookupUserOperator("public", "===", "int4", "int4")
	neOp, _ := im.LookupUserOperator("public", "!==", "int4", "int4")

	if err := runDDL(t, ctx, `ALTER OPERATOR public.=== (int4, int4) SET (NEGATOR = public.!==)`); err != nil {
		t.Fatalf("ALTER OPERATOR SET NEGATOR: %v", err)
	}
	if eqOp.NegatorOID != neOp.OID {
		t.Errorf("===.NegatorOID = %d, want %d", eqOp.NegatorOID, neOp.OID)
	}
	// Back-patched onto the other side.
	if neOp.NegatorOID != eqOp.OID {
		t.Errorf("!==.NegatorOID = %d, want back-patched %d", neOp.NegatorOID, eqOp.OID)
	}

	// No-op restatement of the same negator succeeds.
	if err := runDDL(t, ctx, `ALTER OPERATOR public.=== (int4, int4) SET (NEGATOR = public.!==)`); err != nil {
		t.Errorf("restating same NEGATOR should succeed, got: %v", err)
	}

	// Changing to a different negator is rejected.
	if err := runDDL(t, ctx, `CREATE OPERATOR public.<<>> (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR <<>>: %v", err)
	}
	err := runDDL(t, ctx, `ALTER OPERATOR public.=== (int4, int4) SET (NEGATOR = public.<<>>)`)
	if err == nil || !strings.Contains(err.Error(), `cannot be changed if it has already been set`) {
		t.Fatalf("error = %v, want 'cannot be changed if it has already been set'", err)
	}

	// Self-negation is always rejected, even via ALTER.
	err = runDDL(t, ctx, `ALTER OPERATOR public.<<>> (int4, int4) SET (NEGATOR = public.<<>>)`)
	if err == nil || !strings.Contains(err.Error(), "cannot be its own negator") {
		t.Fatalf("error = %v, want 'cannot be its own negator'", err)
	}
}

// TestAlterOperatorSetMergesHashesOnceOnly mirrors the commutator/negator
// once-only rule for the MERGES/HASHES flags: turning one on that was off is
// fine, restating the same value is a no-op, but turning one off that was on
// is rejected.
func TestAlterOperatorSetMergesHashesOnceOnly(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR public.=== (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	op, _ := im.LookupUserOperator("public", "===", "int4", "int4")

	if err := runDDL(t, ctx, `ALTER OPERATOR public.=== (int4, int4) SET (MERGES, HASHES)`); err != nil {
		t.Fatalf("ALTER OPERATOR SET MERGES,HASHES: %v", err)
	}
	if !op.CanMerge || !op.CanHash {
		t.Fatalf("CanMerge=%v CanHash=%v, want both true", op.CanMerge, op.CanHash)
	}

	if err := runDDL(t, ctx, `ALTER OPERATOR public.=== (int4, int4) SET (MERGES = true)`); err != nil {
		t.Errorf("restating MERGES=true should succeed, got: %v", err)
	}

	err := runDDL(t, ctx, `ALTER OPERATOR public.=== (int4, int4) SET (HASHES = false)`)
	if err == nil || !strings.Contains(err.Error(), `cannot be changed if it has already been set`) {
		t.Fatalf("error = %v, want 'cannot be changed if it has already been set'", err)
	}
}

// TestAlterOperatorSetImmutableAndMissing verifies LEFTARG/RIGHTARG/FUNCTION/
// PROCEDURE are already rejected at parse time (tested in the parser suite),
// and that ALTER OPERATOR on a non-existent operator raises 42883.
func TestAlterOperatorSetMissingOperator(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	err := runDDL(t, ctx, `ALTER OPERATOR public.=== (int4, int4) SET (RESTRICT = eqsel)`)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42883" {
		t.Fatalf("error = %v, want ExecError{Code: 42883}", err)
	}
}

// pgOpfamilyVirtualRows invokes pg_opfamily's VirtualRows function through
// the catalog table registry, mirroring how pg_dump's getOpfamilies reads it.
func pgOpfamilyVirtualRows(t *testing.T, im *catalog.InMemory) [][]string {
	t.Helper()
	tbl, ok := im.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_opfamily"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_catalog.pg_opfamily virtual table/VirtualRows not found")
	}
	return tbl.VirtualRows()
}

// TestCreateOperatorFamily verifies CREATE OPERATOR FAMILY registers a
// catalog.UserOperatorFamily resolving the USING method to its pg_am OID and
// the schema to its namespace OID, and that it surfaces through
// pg_opfamily.VirtualRows (pg_dump's getOpfamilies/dumpOpfamily). DU-002
// slice 408.
func TestCreateOperatorFamily(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR FAMILY public.op_family USING btree`); err != nil {
		t.Fatalf("CREATE OPERATOR FAMILY: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)

	rows := pgOpfamilyVirtualRows(t, im)
	if len(rows) != 1 {
		t.Fatalf("VirtualRows = %d rows, want 1: %v", len(rows), rows)
	}
	row := rows[0]
	if row[1] != "403" { // opfmethod: btree
		t.Errorf("opfmethod = %q, want 403 (btree)", row[1])
	}
	if row[2] != "op_family" { // opfname
		t.Errorf("opfname = %q, want op_family", row[2])
	}
}

// TestCreateOperatorFamilyIdempotent verifies re-registering the same
// (schema, name, method) reuses the same OID (mirrors RegisterUserOperator's
// idempotency), so re-running the CREATE (e.g. IF NOT EXISTS-style retries at
// a higher layer) does not mint a second pg_opfamily row. DU-002 slice 408.
func TestCreateOperatorFamilyIdempotent(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR FAMILY public.op_family USING btree`); err != nil {
		t.Fatalf("first CREATE OPERATOR FAMILY: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	first := im.ListUserOperatorFamilies()[0]
	if err := runDDL(t, ctx, `CREATE OPERATOR FAMILY public.op_family USING btree`); err != nil {
		t.Fatalf("second CREATE OPERATOR FAMILY: %v", err)
	}
	fams := im.ListUserOperatorFamilies()
	if len(fams) != 1 {
		t.Fatalf("ListUserOperatorFamilies = %d entries, want 1: %v", len(fams), fams)
	}
	if fams[0].OID != first.OID {
		t.Errorf("OID changed across idempotent re-CREATE: %d -> %d", first.OID, fams[0].OID)
	}
}

// TestCreateOperatorFamilyUnknownMethod verifies an unrecognized USING method
// raises 42704 "access method ... does not exist", mirroring PG's
// get_index_am_oid(amname, false) check in CreateOpFamily (opfamilycmds.c).
// DU-002 slice 408.
func TestCreateOperatorFamilyUnknownMethod(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	err := runDDL(t, ctx, `CREATE OPERATOR FAMILY public.op_family USING bogus_am`)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42704" {
		t.Fatalf("error = %v, want ExecError{Code: 42704}", err)
	}
}

// pgOpclassVirtualRows fetches the current pg_opclass virtual view rows,
// mirroring pgOpfamilyVirtualRows above. DU-002 (M0119-0004).
func pgOpclassVirtualRows(t *testing.T, im *catalog.InMemory) [][]string {
	t.Helper()
	tbl, ok := im.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_opclass"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_catalog.pg_opclass virtual table/VirtualRows not found")
	}
	return tbl.VirtualRows()
}

// TestCreateOperatorClassPopulatesOpclassRow verifies CREATE OPERATOR CLASS
// FOR TYPE t USING method FAMILY family AS STORAGE t2 populates a full
// pg_opclass row (opcmethod/opcname/opcnamespace/opcowner/opcfamily/
// opcintype/opcdefault/opckeytype), the DU-002 (M0119-0004) op_class_empty
// slice: no OPERATOR/FUNCTION members, so no pg_amop/pg_amproc store is
// needed to round-trip this shape through pg_dump.
func TestCreateOperatorClassPopulatesOpclassRow(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR FAMILY public.op_family USING btree`); err != nil {
		t.Fatalf("CREATE OPERATOR FAMILY: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE OPERATOR CLASS public.op_class_empty FOR TYPE bigint USING btree FAMILY public.op_family AS STORAGE bigint`); err != nil {
		t.Fatalf("CREATE OPERATOR CLASS: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)

	famRows := pgOpfamilyVirtualRows(t, im)
	if len(famRows) != 1 {
		t.Fatalf("pg_opfamily VirtualRows = %d rows, want 1: %v", len(famRows), famRows)
	}
	famOID := famRows[0][0]

	classRows := pgOpclassVirtualRows(t, im)
	if len(classRows) != 1 {
		t.Fatalf("pg_opclass VirtualRows = %d rows, want 1: %v", len(classRows), classRows)
	}
	row := classRows[0]
	// oid, opcmethod, opcname, opcnamespace, opcowner, opcfamily, opcintype, opcdefault, opckeytype
	if row[1] != "403" { // opcmethod: btree
		t.Errorf("opcmethod = %q, want 403 (btree)", row[1])
	}
	if row[2] != "op_class_empty" { // opcname
		t.Errorf("opcname = %q, want op_class_empty", row[2])
	}
	if row[5] != famOID { // opcfamily must resolve to the explicit FAMILY clause's OID
		t.Errorf("opcfamily = %q, want %q (public.op_family's OID)", row[5], famOID)
	}
	if row[6] != "20" { // opcintype: bigint (int8) OID
		t.Errorf("opcintype = %q, want 20 (bigint)", row[6])
	}
	if row[7] != "f" { // opcdefault: DEFAULT not specified
		t.Errorf("opcdefault = %q, want f", row[7])
	}
	if row[8] != "20" { // opckeytype: STORAGE bigint -> bigint (int8) OID
		t.Errorf("opckeytype = %q, want 20 (bigint)", row[8])
	}
}

// TestCreateOperatorClassAutoCreatesFamily verifies that omitting the
// explicit FAMILY clause auto-creates an anonymous family sharing the
// class's own name (PG's DefineOpClass, opclasscmds.c) — opcfamily is
// NOT NULL, so every opclass needs a valid family even when the user never
// wrote CREATE OPERATOR FAMILY. DU-002 (M0119-0004).
func TestCreateOperatorClassAutoCreatesFamily(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR CLASS public.op_class_custom FOR TYPE int4 USING btree AS STORAGE int4`); err != nil {
		t.Fatalf("CREATE OPERATOR CLASS: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)

	famRows := pgOpfamilyVirtualRows(t, im)
	if len(famRows) != 1 {
		t.Fatalf("pg_opfamily VirtualRows = %d rows, want 1 (auto-created): %v", len(famRows), famRows)
	}
	if famRows[0][2] != "op_class_custom" {
		t.Errorf("auto-created opfname = %q, want op_class_custom (same name as the class)", famRows[0][2])
	}

	classRows := pgOpclassVirtualRows(t, im)
	if len(classRows) != 1 {
		t.Fatalf("pg_opclass VirtualRows = %d rows, want 1: %v", len(classRows), classRows)
	}
	if classRows[0][5] != famRows[0][0] { // opcfamily must point at the auto-created family's OID
		t.Errorf("opcfamily = %q, want %q (auto-created family OID)", classRows[0][5], famRows[0][0])
	}
}

// TestCreateOperatorClassUnknownFamily verifies an explicit FAMILY clause
// naming a family that was never CREATEd raises 42704, mirroring
// opfamilycmds.c's own "operator family ... does not exist" check.
// DU-002 (M0119-0004).
func TestCreateOperatorClassUnknownFamily(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	err := runDDL(t, ctx, `CREATE OPERATOR CLASS public.op_class_empty FOR TYPE bigint USING btree FAMILY public.no_such_family AS STORAGE bigint`)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42704" {
		t.Fatalf("error = %v, want ExecError{Code: 42704}", err)
	}
}

func pgAmopVirtualRows(t *testing.T, im *catalog.InMemory) [][]string {
	t.Helper()
	tbl, ok := im.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_amop"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_catalog.pg_amop virtual table/VirtualRows not found")
	}
	return tbl.VirtualRows()
}

func pgAmprocVirtualRows(t *testing.T, im *catalog.InMemory) [][]string {
	t.Helper()
	tbl, ok := im.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_amproc"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_catalog.pg_amproc virtual table/VirtualRows not found")
	}
	return tbl.VirtualRows()
}

func pgDependVirtualRows(t *testing.T, im *catalog.InMemory) [][]string {
	t.Helper()
	tbl, ok := im.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_depend"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_catalog.pg_depend virtual table/VirtualRows not found")
	}
	return tbl.VirtualRows()
}

// TestCreateOperatorClassMembersPopulateAmopAmproc verifies a CREATE
// OPERATOR CLASS AS-list OPERATOR/FUNCTION entry naming a resolvable
// (user-defined operator / curated-builtin function) reference populates a
// real pg_amop/pg_amproc row plus the two pg_depend rows dumpOpclass's own
// query needs (classid=pg_amop/pg_amproc → refclassid=pg_operator/pg_proc,
// deptype NORMAL; classid=pg_amop/pg_amproc → refclassid=pg_opclass,
// deptype INTERNAL), closing the slice-409 ledger row's member-store resume
// point. DU-002 (M0119-0004) slice 411.
func TestCreateOperatorClassMembersPopulateAmopAmproc(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR public.~=~ (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE OPERATOR FAMILY public.op_family USING btree`); err != nil {
		t.Fatalf("CREATE OPERATOR FAMILY: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE OPERATOR CLASS public.op_class FOR TYPE int4 USING btree FAMILY public.op_family AS
		OPERATOR 1 ~=~ (int4, int4),
		FUNCTION 1 int4eq(int4, int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR CLASS: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)

	famRows := pgOpfamilyVirtualRows(t, im)
	classRows := pgOpclassVirtualRows(t, im)
	if len(famRows) != 1 || len(classRows) != 1 {
		t.Fatalf("fam/class rows = %d/%d, want 1/1", len(famRows), len(classRows))
	}
	famOID, classOID := famRows[0][0], classRows[0][0]

	op, ok := im.LookupUserOperator("public", "~=~", "int4", "int4")
	if !ok {
		t.Fatal("~=~ not registered")
	}

	amopRows := pgAmopVirtualRows(t, im)
	if len(amopRows) != 1 {
		t.Fatalf("pg_amop VirtualRows = %d rows, want 1: %v", len(amopRows), amopRows)
	}
	// oid, amopfamily, amoplefttype, amoprighttype, amopstrategy, amoppurpose, amopopr, amopmethod, amopsortfamily
	amop := amopRows[0]
	if amop[1] != famOID {
		t.Errorf("amopfamily = %q, want %q", amop[1], famOID)
	}
	if amop[2] != "23" || amop[3] != "23" { // int4 OID
		t.Errorf("amoplefttype/amoprighttype = %q/%q, want 23/23", amop[2], amop[3])
	}
	if amop[4] != "1" {
		t.Errorf("amopstrategy = %q, want 1", amop[4])
	}
	if amop[5] != "s" {
		t.Errorf("amoppurpose = %q, want s", amop[5])
	}
	if amop[6] != strconv.FormatUint(uint64(op.OID), 10) {
		t.Errorf("amopopr = %q, want %q (~=~'s OID)", amop[6], strconv.FormatUint(uint64(op.OID), 10))
	}
	if amop[7] != "403" { // btree
		t.Errorf("amopmethod = %q, want 403 (btree)", amop[7])
	}

	amprocRows := pgAmprocVirtualRows(t, im)
	if len(amprocRows) != 1 {
		t.Fatalf("pg_amproc VirtualRows = %d rows, want 1: %v", len(amprocRows), amprocRows)
	}
	// oid, amprocfamily, amproclefttype, amprocrighttype, amprocnum, amproc
	amproc := amprocRows[0]
	if amproc[1] != famOID {
		t.Errorf("amprocfamily = %q, want %q", amproc[1], famOID)
	}
	if amproc[4] != "1" {
		t.Errorf("amprocnum = %q, want 1", amproc[4])
	}
	if amproc[5] != "65" { // int4eq's curated builtin OID
		t.Errorf("amproc = %q, want 65 (int4eq)", amproc[5])
	}

	// pg_depend: amop → operator (NORMAL) + amop → opclass (INTERNAL);
	// amproc → proc (NORMAL) + amproc → opclass (INTERNAL). DU-002 slice 411.
	dependRows := pgDependVirtualRows(t, im)
	want := map[string]bool{
		"2602|" + amop[0] + "|2617|" + strconv.FormatUint(uint64(op.OID), 10) + "|n": false,
		"2602|" + amop[0] + "|2616|" + classOID + "|i":                               false,
		"2603|" + amproc[0] + "|1255|65|n":                                           false,
		"2603|" + amproc[0] + "|2616|" + classOID + "|i":                             false,
	}
	for _, r := range dependRows {
		key := r[0] + "|" + r[1] + "|" + r[3] + "|" + r[4] + "|" + r[6]
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("missing expected pg_depend row %q; got %v", k, dependRows)
		}
	}
}

// TestCreateOperatorClassMemberUnresolvableBuiltinDropped documents the
// slice-411 scope boundary: an OPERATOR entry naming a builtin operator
// (goopg has no builtin-operator catalog — deferred, see the ledger) is
// silently dropped rather than raising an error, matching the pre-slice-411
// behavior for every AS-list entry. DU-002 (M0119-0004) slice 411.
func TestCreateOperatorClassMemberUnresolvableBuiltinDropped(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR CLASS public.op_class FOR TYPE int4 USING btree AS OPERATOR 1 =`); err != nil {
		t.Fatalf("CREATE OPERATOR CLASS: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	if rows := pgAmopVirtualRows(t, im); len(rows) != 0 {
		t.Fatalf("pg_amop VirtualRows = %v, want empty (unresolvable builtin operator)", rows)
	}
}

// TestDropOperatorClassRemovesMembers verifies DROP OPERATOR CLASS cascades
// to remove its pg_amop/pg_amproc member rows, so a create-then-drop-then-
// dump sequence doesn't leave ghost members behind (mirrors the existing
// pg_opclass-row cleanup this DROP path already did). DU-002 (M0119-0004)
// slice 411.
func TestDropOperatorClassRemovesMembers(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR public.~=~ (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE OPERATOR CLASS public.op_class FOR TYPE int4 USING btree AS OPERATOR 1 ~=~ (int4, int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR CLASS: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	if rows := pgAmopVirtualRows(t, im); len(rows) != 1 {
		t.Fatalf("pg_amop VirtualRows = %v, want 1 row before DROP", rows)
	}
	if err := runDDL(t, ctx, `DROP OPERATOR CLASS public.op_class USING btree`); err != nil {
		t.Fatalf("DROP OPERATOR CLASS: %v", err)
	}
	if rows := pgAmopVirtualRows(t, im); len(rows) != 0 {
		t.Fatalf("pg_amop VirtualRows = %v, want empty after DROP OPERATOR CLASS", rows)
	}
}
