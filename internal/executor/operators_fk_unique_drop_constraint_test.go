package executor

import (
	"strings"
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

	t.Run("NotNull", func(t *testing.T) {
		// M0134-0005 S04: `DROP CONSTRAINT <name>` on a NOT NULL constraint
		// (contype='n', PG 18+) previously fell through every branch to the
		// PK arm's 42704 even though the constraint existed —
		// tbl.NotNullConstraints was never consulted.
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE dcnn_t (id integer PRIMARY KEY, name text NOT NULL)`); err != nil {
			t.Fatalf("CREATE TABLE dcnn_t: %v", err)
		}

		tbl, ok := cat.LookupTable(parser.ObjectName{Name: "dcnn_t"})
		if !ok {
			t.Fatal("dcnn_t table not found")
		}
		// Auto-name is `<table>_<col>_not_null` (lowercased).
		if err := runDDL(t, ctx, `ALTER TABLE dcnn_t DROP CONSTRAINT dcnn_t_name_not_null`); err != nil {
			t.Fatalf("DROP CONSTRAINT dcnn_t_name_not_null: %v", err)
		}
		for _, nc := range tbl.NotNullConstraints {
			if strings.EqualFold(nc.ColName, "name") {
				t.Fatalf("expected dcnn_t_name_not_null constraint gone, still present: %+v", nc)
			}
		}
		for _, col := range tbl.Columns {
			if strings.EqualFold(col.Name, "name") && col.NotNull {
				t.Fatal("expected name column's NotNull flag cleared")
			}
		}

		// The NOT NULL check must actually be gone: a NULL now inserts.
		if err := runDDL(t, ctx, `INSERT INTO dcnn_t VALUES (1, NULL)`); err != nil {
			t.Fatalf("INSERT with NULL name (no more NOT NULL constraint) should succeed: %v", err)
		}

		// Dropping again must report undefined_object, not silently succeed.
		err := runDDL(t, ctx, `ALTER TABLE dcnn_t DROP CONSTRAINT dcnn_t_name_not_null`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42704" {
			t.Fatalf("expected 42704 on re-drop, got: %v", err)
		}
	})

	t.Run("NotNullPkMemberRefused", func(t *testing.T) {
		// A NOT NULL constraint backing a PRIMARY KEY column cannot be
		// dropped by name — PG's dropconstraint_internal
		// (tablecmds.c:14154-14159) raises 42P16 "column %q is in a primary
		// key" before resetting attnotnull.
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE dcnnpk_t (id integer PRIMARY KEY)`); err != nil {
			t.Fatalf("CREATE TABLE dcnnpk_t: %v", err)
		}

		err := runDDL(t, ctx, `ALTER TABLE dcnnpk_t DROP CONSTRAINT dcnnpk_t_id_not_null`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42P16" {
			t.Fatalf("expected 42P16, got: %v", err)
		}
		if ee.Message != `column "id" is in a primary key` {
			t.Fatalf("unexpected message: %q", ee.Message)
		}
	})

	t.Run("NotNullReplicaIdentityIndexMemberRefused", func(t *testing.T) {
		// A NOT NULL constraint backing a column that participates in the
		// index chosen as the table's replica identity cannot be dropped by
		// name — dropconstraint_internal (tablecmds.c:14161-14167) raises
		// 42P16 "column %q is in index used as replica identity" before
		// resetting attnotnull. M0134-0005am.
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE dcnnri_t (a integer NOT NULL, b integer)`); err != nil {
			t.Fatalf("CREATE TABLE dcnnri_t: %v", err)
		}
		if err := runDDL(t, ctx, `CREATE UNIQUE INDEX dcnnri_t_uidx ON dcnnri_t (a)`); err != nil {
			t.Fatalf("CREATE UNIQUE INDEX dcnnri_t_uidx: %v", err)
		}
		if err := runDDL(t, ctx, `ALTER TABLE dcnnri_t REPLICA IDENTITY USING INDEX dcnnri_t_uidx`); err != nil {
			t.Fatalf("ALTER TABLE dcnnri_t REPLICA IDENTITY USING INDEX dcnnri_t_uidx: %v", err)
		}

		err := runDDL(t, ctx, `ALTER TABLE dcnnri_t DROP CONSTRAINT dcnnri_t_a_not_null`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42P16" {
			t.Fatalf("expected 42P16, got: %v", err)
		}
		if ee.Message != `column "a" is in index used as replica identity` {
			t.Fatalf("unexpected message: %q", ee.Message)
		}
	})

	t.Run("NotNullIdentityColumnRefused", func(t *testing.T) {
		// A NOT NULL constraint backing a GENERATED ... AS IDENTITY column
		// cannot be dropped by name — dropconstraint_internal
		// (tablecmds.c:14169-14181) raises 55000 "column %q of relation %q
		// is an identity column" before resetting attnotnull. M0134-0005am.
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE dcnnid_t (id int GENERATED ALWAYS AS IDENTITY NOT NULL, b integer)`); err != nil {
			t.Fatalf("CREATE TABLE dcnnid_t: %v", err)
		}

		err := runDDL(t, ctx, `ALTER TABLE dcnnid_t DROP CONSTRAINT dcnnid_t_id_not_null`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "55000" {
			t.Fatalf("expected 55000, got: %v", err)
		}
		if ee.Message != `column "id" of relation "dcnnid_t" is an identity column` {
			t.Fatalf("unexpected message: %q", ee.Message)
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
