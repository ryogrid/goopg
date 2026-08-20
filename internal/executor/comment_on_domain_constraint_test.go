package executor

// comment_on_domain_constraint_test.go pins the M0134-0005ai fix:
// `COMMENT ON CONSTRAINT <name> ON DOMAIN <domain> IS '...'` now parses,
// resolves, and writes/erases a pg_description row, and both constraint
// spellings (ON <table> and ON DOMAIN <domain>) now enforce object
// ownership. Before this fix the ON DOMAIN spelling failed to parse and fell
// through to the blind-success compatNoopCommandTag fallback
// (postmaster/dispatch.go), which returned CommandComplete("COMMENT") with
// zero catalog access — every positive-path assertion here therefore checks
// the pg_description row directly (not just statement success), per the
// brief's "the trap" note: matching wire output is not proof of a working
// mechanism.
//
// PG oracle: postgres/src/test/regress/sql/constraints.sql:1018-1046,
// postgres/src/test/regress/expected/constraints.out:1681-1706;
// postgres/src/backend/catalog/objectaddress.c (OBJECT_DOMCONSTRAINT,
// :975-990); postgres/src/backend/catalog/pg_constraint.c
// (get_domain_constraint_oid, :1391,1427); postgres/src/backend/catalog/
// aclchk.c (aclcheck_error, ACLCHECK_NOT_OWNER).

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

const commentOnDomainConstraintOidPgConstraint = uint32(2606)

// TestCommentOnDomainConstraintWritesDescription is the positive path: the
// pg_description row must actually exist, not just "the statement succeeded"
// (see the file-level trap note — a blind-success fallback would also print
// COMMENT here).
func TestCommentOnDomainConstraintWritesDescription(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	if err := runDDL(t, ctx, `CREATE DOMAIN cc_dom AS int CONSTRAINT the_constraint CHECK (value > 0)`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	if err := runDDL(t, ctx, `COMMENT ON CONSTRAINT the_constraint ON DOMAIN cc_dom IS 'yes, another comment'`); err != nil {
		t.Fatalf("COMMENT ON CONSTRAINT ... ON DOMAIN: %v", err)
	}

	dom, ok := im.LookupDomain("cc_dom", ctx.CurrentDatabaseOid)
	if !ok {
		t.Fatalf("catalog lost domain cc_dom")
	}
	var constrOID uint32
	for _, ck := range dom.Checks {
		if ck.Name == "the_constraint" {
			constrOID = ck.OID
		}
	}
	if constrOID == 0 {
		t.Fatalf("domain constraint the_constraint not found on cc_dom")
	}
	desc, ok := im.GetComment(commentOnDomainConstraintOidPgConstraint, constrOID, 0)
	if !ok || desc != "yes, another comment" {
		t.Fatalf("GetComment(pg_constraint, %d, 0) = (%q, %v), want (%q, true)", constrOID, desc, ok, "yes, another comment")
	}
}

// TestCommentOnDomainConstraintIsNullRemovesDescription: IS NULL must delete
// the description row, matching the existing table-constraint arm.
func TestCommentOnDomainConstraintIsNullRemovesDescription(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	if err := runDDL(t, ctx, `CREATE DOMAIN cc_dom AS int CONSTRAINT the_constraint CHECK (value > 0)`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	if err := runDDL(t, ctx, `COMMENT ON CONSTRAINT the_constraint ON DOMAIN cc_dom IS 'yes, another comment'`); err != nil {
		t.Fatalf("COMMENT ON CONSTRAINT ... ON DOMAIN (set): %v", err)
	}
	if err := runDDL(t, ctx, `COMMENT ON CONSTRAINT the_constraint ON DOMAIN cc_dom IS NULL`); err != nil {
		t.Fatalf("COMMENT ON CONSTRAINT ... ON DOMAIN (NULL): %v", err)
	}

	dom, ok := im.LookupDomain("cc_dom", ctx.CurrentDatabaseOid)
	if !ok {
		t.Fatalf("catalog lost domain cc_dom")
	}
	var constrOID uint32
	for _, ck := range dom.Checks {
		if ck.Name == "the_constraint" {
			constrOID = ck.OID
		}
	}
	if constrOID == 0 {
		t.Fatalf("domain constraint the_constraint not found on cc_dom")
	}
	if desc, ok := im.GetComment(commentOnDomainConstraintOidPgConstraint, constrOID, 0); ok {
		t.Fatalf("GetComment after IS NULL = (%q, %v), want row removed (false)", desc, ok)
	}
}

// TestCommentOnDomainConstraintMissingDomainErrors: PG resolves the domain
// type FIRST (objectaddress.c OBJECT_DOMCONSTRAINT), so a missing domain
// reports the "type ... does not exist" error, not the constraint one.
func TestCommentOnDomainConstraintMissingDomainErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, `COMMENT ON CONSTRAINT the_constraint ON DOMAIN no_comments_dom IS 'another bad comment'`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42704" {
		t.Errorf("Code=%q want 42704", ee.Code)
	}
	if want := `type "no_comments_dom" does not exist`; ee.Message != want {
		t.Errorf("Message=%q want %q", ee.Message, want)
	}
}

// TestCommentOnDomainConstraintMissingConstraintErrors: the domain exists but
// the named constraint does not.
func TestCommentOnDomainConstraintMissingConstraintErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE DOMAIN cc_dom AS int CONSTRAINT the_constraint CHECK (value > 0)`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	err := runDDL(t, ctx, `COMMENT ON CONSTRAINT no_constraint ON DOMAIN cc_dom IS 'yes, another comment'`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42704" {
		t.Errorf("Code=%q want 42704", ee.Code)
	}
	if want := `constraint "no_constraint" for domain cc_dom does not exist`; ee.Message != want {
		t.Errorf("Message=%q want %q", ee.Message, want)
	}
}

// TestCommentOnConstraintNonOwnerRejected: the ON <table> spelling, run by a
// non-owner non-superuser role, must be rejected with 42501 and the object
// name UNQUOTED.
func TestCommentOnConstraintNonOwnerRejected(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	im.RegisterRole("regress_constraint_comments")
	im.RegisterRole("regress_constraint_comments_noaccess")

	ctx.NonSuperuserRole = "regress_constraint_comments"
	if err := runDDL(t, ctx, `CREATE TABLE constraint_comments_tbl (a int CONSTRAINT the_constraint CHECK (a > 0))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	ctx.NonSuperuserRole = "regress_constraint_comments_noaccess"
	err := runDDL(t, ctx, `COMMENT ON CONSTRAINT the_constraint ON constraint_comments_tbl IS 'no, the comment'`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42501" {
		t.Errorf("Code=%q want 42501", ee.Code)
	}
	if want := `must be owner of relation constraint_comments_tbl`; ee.Message != want {
		t.Errorf("Message=%q want %q", ee.Message, want)
	}
}

// TestCommentOnDomainConstraintNonOwnerRejected: the ON DOMAIN spelling's
// sibling of the above — the exact 42501 wording differs ("type" not
// "relation").
func TestCommentOnDomainConstraintNonOwnerRejected(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	im.RegisterRole("regress_constraint_comments")
	im.RegisterRole("regress_constraint_comments_noaccess")

	ctx.NonSuperuserRole = "regress_constraint_comments"
	if err := runDDL(t, ctx, `CREATE DOMAIN constraint_comments_dom AS int CONSTRAINT the_constraint CHECK (value > 0)`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}

	ctx.NonSuperuserRole = "regress_constraint_comments_noaccess"
	err := runDDL(t, ctx, `COMMENT ON CONSTRAINT the_constraint ON DOMAIN constraint_comments_dom IS 'no, another comment'`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42501" {
		t.Errorf("Code=%q want 42501", ee.Code)
	}
	if want := `must be owner of type constraint_comments_dom`; ee.Message != want {
		t.Errorf("Message=%q want %q", ee.Message, want)
	}
}

// TestCommentOnConstraintOwnerStillAllowed is the mandatory over-fix guard:
// the owning role (and a superuser) must still succeed and still write the
// row, for BOTH spellings, proving the ownership check didn't turn into a
// blanket deny.
func TestCommentOnConstraintOwnerStillAllowed(t *testing.T) {
	t.Run("owning role, table spelling", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()
		im := cat.(*catalog.InMemory)
		im.RegisterRole("alice")

		ctx.NonSuperuserRole = "alice"
		if err := runDDL(t, ctx, `CREATE TABLE cc_owner_tbl (a int CONSTRAINT ck1 CHECK (a > 0))`); err != nil {
			t.Fatalf("CREATE TABLE: %v", err)
		}
		if err := runDDL(t, ctx, `COMMENT ON CONSTRAINT ck1 ON cc_owner_tbl IS 'owner comment'`); err != nil {
			t.Fatalf("COMMENT ON CONSTRAINT by owner: %v", err)
		}
		tbl, ok := im.LookupTable(parser.ObjectName{Name: "cc_owner_tbl"}, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
		if !ok {
			t.Fatalf("catalog lost cc_owner_tbl")
		}
		var constrOID uint32
		for _, nc := range tbl.NamedChecks {
			if nc.Name == "ck1" {
				constrOID = nc.OID
			}
		}
		if constrOID == 0 {
			t.Fatalf("constraint ck1 not found")
		}
		if desc, ok := im.GetComment(commentOnDomainConstraintOidPgConstraint, constrOID, 0); !ok || desc != "owner comment" {
			t.Fatalf("GetComment = (%q, %v), want (%q, true)", desc, ok, "owner comment")
		}
	})

	t.Run("superuser, domain spelling", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()
		im := cat.(*catalog.InMemory)
		im.RegisterRole("alice")

		ctx.NonSuperuserRole = "alice"
		if err := runDDL(t, ctx, `CREATE DOMAIN cc_owner_dom AS int CONSTRAINT ck1 CHECK (value > 0)`); err != nil {
			t.Fatalf("CREATE DOMAIN: %v", err)
		}

		// Superuser session (ctx.NonSuperuserRole reset to "") must still be
		// able to comment on a domain owned by someone else.
		ctx.NonSuperuserRole = ""
		if err := runDDL(t, ctx, `COMMENT ON CONSTRAINT ck1 ON DOMAIN cc_owner_dom IS 'super comment'`); err != nil {
			t.Fatalf("COMMENT ON CONSTRAINT by superuser: %v", err)
		}
		dom, ok := im.LookupDomain("cc_owner_dom", ctx.CurrentDatabaseOid)
		if !ok {
			t.Fatalf("catalog lost cc_owner_dom")
		}
		var constrOID uint32
		for _, ck := range dom.Checks {
			if ck.Name == "ck1" {
				constrOID = ck.OID
			}
		}
		if constrOID == 0 {
			t.Fatalf("constraint ck1 not found on cc_owner_dom")
		}
		if desc, ok := im.GetComment(commentOnDomainConstraintOidPgConstraint, constrOID, 0); !ok || desc != "super comment" {
			t.Fatalf("GetComment = (%q, %v), want (%q, true)", desc, ok, "super comment")
		}
	})
}
