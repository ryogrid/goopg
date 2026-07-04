package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestDropConstraintForeignKeyAndUnique covers the slice-433 follow-up
// deferral: `ALTER TABLE t DROP CONSTRAINT name` previously only searched
// NamedChecks and PRIMARY KEY indexes by name, so dropping a real FOREIGN
// KEY or UNIQUE constraint by name misreported 42704 "does not exist" even
// though the constraint existed. execAlterTableDropConstraint
// (internal/executor/operators_ddl.go) now also searches tbl.ForeignKeys and
// non-primary Unique/IsConstraint indexes before falling through to the
// PRIMARY KEY branch's 42704, backed by the new
// catalog.InMemory.DropForeignKeyConstraint / DropUniqueConstraint.
func TestDropConstraintForeignKeyAndUnique(t *testing.T) {
	t.Run("ForeignKey", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE dcfk_parent (id integer PRIMARY KEY)`); err != nil {
			t.Fatalf("CREATE TABLE dcfk_parent: %v", err)
		}
		if err := runDDL(t, ctx, `CREATE TABLE dcfk_child (id integer, pid integer)`); err != nil {
			t.Fatalf("CREATE TABLE dcfk_child: %v", err)
		}
		if err := runDDL(t, ctx, `ALTER TABLE dcfk_child ADD CONSTRAINT dcfk_fk FOREIGN KEY (pid) REFERENCES dcfk_parent(id)`); err != nil {
			t.Fatalf("ADD CONSTRAINT: %v", err)
		}

		tbl, ok := cat.LookupTable(parser.ObjectName{Name: "dcfk_child"})
		if !ok {
			t.Fatal("dcfk_child table not found")
		}
		if len(tbl.ForeignKeys) != 1 {
			t.Fatalf("expected 1 FK before drop, got %d", len(tbl.ForeignKeys))
		}

		if err := runDDL(t, ctx, `ALTER TABLE dcfk_child DROP CONSTRAINT dcfk_fk`); err != nil {
			t.Fatalf("DROP CONSTRAINT dcfk_fk: %v", err)
		}
		if len(tbl.ForeignKeys) != 0 {
			t.Fatalf("expected 0 FKs after drop, got %d: %+v", len(tbl.ForeignKeys), tbl.ForeignKeys)
		}

		// The FK's runtime check must actually be gone, not just the catalog
		// bookkeeping — insert a row with a now-unchecked dangling reference.
		if err := runDDL(t, ctx, `INSERT INTO dcfk_child VALUES (1, 999)`); err != nil {
			t.Fatalf("INSERT after DROP CONSTRAINT should succeed (no more FK check): %v", err)
		}

		// Dropping again must report undefined_object, not silently succeed.
		err := runDDL(t, ctx, `ALTER TABLE dcfk_child DROP CONSTRAINT dcfk_fk`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42704" {
			t.Fatalf("expected 42704 on re-drop, got: %v", err)
		}
	})

	t.Run("Unique", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE dcuq_t (id integer PRIMARY KEY, email text)`); err != nil {
			t.Fatalf("CREATE TABLE dcuq_t: %v", err)
		}
		if err := runDDL(t, ctx, `ALTER TABLE dcuq_t ADD CONSTRAINT dcuq_email_key UNIQUE (email)`); err != nil {
			t.Fatalf("ADD CONSTRAINT UNIQUE: %v", err)
		}

		if _, ok := cat.LookupIndex(parser.ObjectName{Name: "dcuq_email_key"}); !ok {
			t.Fatal("dcuq_email_key index not found before drop")
		}

		if err := runDDL(t, ctx, `ALTER TABLE dcuq_t DROP CONSTRAINT dcuq_email_key`); err != nil {
			t.Fatalf("DROP CONSTRAINT dcuq_email_key: %v", err)
		}

		if _, ok := cat.LookupIndex(parser.ObjectName{Name: "dcuq_email_key"}); ok {
			t.Fatal("dcuq_email_key index still present after drop")
		}

		// The uniqueness check must actually be gone: two equal emails now
		// insert without conflict.
		if err := runDDL(t, ctx, `INSERT INTO dcuq_t VALUES (1, 'a@example.com')`); err != nil {
			t.Fatalf("INSERT 1: %v", err)
		}
		if err := runDDL(t, ctx, `INSERT INTO dcuq_t VALUES (2, 'a@example.com')`); err != nil {
			t.Fatalf("INSERT 2 (duplicate email, no more UNIQUE constraint) should succeed: %v", err)
		}

		// The table's PRIMARY KEY must be untouched by dropping the sibling
		// UNIQUE constraint (regression guard against over-broad removal).
		if _, ok := cat.LookupIndex(parser.ObjectName{Name: "dcuq_t_pkey"}); !ok {
			t.Fatal("dcuq_t_pkey should still exist after dropping the unrelated UNIQUE constraint")
		}
	})

	t.Run("Exclude", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE dcex_t (c1 int, c2 int,
			CONSTRAINT dcex_excl EXCLUDE USING btree (c1 WITH =))`); err != nil {
			t.Fatalf("CREATE TABLE dcex_t: %v", err)
		}

		if _, ok := cat.LookupIndex(parser.ObjectName{Name: "dcex_excl"}); !ok {
			t.Fatal("dcex_excl index not found before drop")
		}

		// Confirm the exclusion check is live before the drop (sanity, mirrors
		// TestExclusionConstraintBtreeEqualityFires).
		if err := runDDL(t, ctx, `INSERT INTO dcex_t VALUES (1, 2)`); err != nil {
			t.Fatalf("INSERT 1: %v", err)
		}
		err := runDDL(t, ctx, `INSERT INTO dcex_t VALUES (1, 20)`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "23P01" {
			t.Fatalf("expected 23P01 exclusion violation before drop, got: %v", err)
		}

		if err := runDDL(t, ctx, `ALTER TABLE dcex_t DROP CONSTRAINT dcex_excl`); err != nil {
			t.Fatalf("DROP CONSTRAINT dcex_excl: %v", err)
		}

		if _, ok := cat.LookupIndex(parser.ObjectName{Name: "dcex_excl"}); ok {
			t.Fatal("dcex_excl index still present after drop")
		}

		// The exclusion check must actually be gone: the same duplicate c1 now
		// inserts without conflict.
		if err := runDDL(t, ctx, `INSERT INTO dcex_t VALUES (1, 20)`); err != nil {
			t.Fatalf("INSERT (duplicate c1, no more EXCLUDE constraint) should succeed: %v", err)
		}

		// Dropping again must report undefined_object, not silently succeed.
		err = runDDL(t, ctx, `ALTER TABLE dcex_t DROP CONSTRAINT dcex_excl`)
		ee, ok = err.(*ExecError)
		if !ok || ee.Code != "42704" {
			t.Fatalf("expected 42704 on re-drop, got: %v", err)
		}
	})

	t.Run("UndefinedConstraintStillRejected", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE dcund_t (a integer)`); err != nil {
			t.Fatalf("CREATE TABLE dcund_t: %v", err)
		}
		err := runDDL(t, ctx, `ALTER TABLE dcund_t DROP CONSTRAINT nosuchconstraint`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42704" {
			t.Fatalf("expected 42704 undefined_object, got: %v", err)
		}
	})
}
