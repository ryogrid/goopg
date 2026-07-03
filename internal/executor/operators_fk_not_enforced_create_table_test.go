package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestFKNotEnforcedCreateTableTime covers DU-002 slice 432: extends slice
// 431's ALTER TABLE-only `NOT ENFORCED`/`NOT VALID` FK support to CREATE
// TABLE-time FK constraints — both the inline column REFERENCES form and the
// table-level FOREIGN KEY form. Verifies catalog.ForeignKey.NotEnforced,
// pg_constraint conenforced/convalidated, pg_get_constraintdef rendering, and
// the runtime skip (no RI trigger fires for a NOT ENFORCED FK), mirroring
// TestFKNotEnforcedAlterTable/TestFKNotEnforcedSkipsRuntimeCheck.
func TestFKNotEnforcedCreateTableTime(t *testing.T) {
	t.Run("InlineColumnNotEnforced", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE ctnenf_parent (id integer)`); err != nil {
			t.Fatalf("CREATE TABLE ctnenf_parent: %v", err)
		}
		if err := runDDL(t, ctx, `CREATE TABLE ctnenf_child (id integer, pid integer REFERENCES ctnenf_parent(id) NOT ENFORCED)`); err != nil {
			t.Fatalf("CREATE TABLE ctnenf_child: %v", err)
		}

		tbl, ok := cat.LookupTable(parser.ObjectName{Name: "ctnenf_child"})
		if !ok {
			t.Fatal("ctnenf_child table not found")
		}
		if len(tbl.ForeignKeys) != 1 {
			t.Fatalf("expected 1 foreign key, got %+v", tbl.ForeignKeys)
		}
		if !tbl.ForeignKeys[0].NotEnforced {
			t.Fatalf("expected ForeignKeys[0].NotEnforced=true, got %+v", tbl.ForeignKeys[0])
		}

		pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
		if !ok || pgcon.VirtualRows == nil {
			t.Fatal("pg_constraint virtual table not found")
		}
		var row []string
		for _, r := range pgcon.VirtualRows() {
			if r[3] == "f" && r[1] == tbl.ForeignKeys[0].Name {
				row = r
			}
		}
		if row == nil {
			t.Fatal("no pg_constraint row for the inline FK")
		}
		if row[6] != "f" {
			t.Errorf("convalidated = %q, want f (NOT ENFORCED implies unvalidated)", row[6])
		}
		if row[25] != "f" {
			t.Errorf("conenforced = %q, want f", row[25])
		}

		rows := runQuery(t, ctx, `SELECT pg_get_constraintdef(`+row[0]+`::oid)`)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row from pg_get_constraintdef, got %d", len(rows))
		}
		def := rows[0][0].StringValue()
		if !strings.Contains(def, "NOT ENFORCED") {
			t.Errorf("pg_get_constraintdef = %q, want it to contain NOT ENFORCED", def)
		}

		// Runtime skip: a dangling reference must be allowed under NOT ENFORCED.
		if err := runDDL(t, ctx, `INSERT INTO ctnenf_child VALUES (1, 999)`); err != nil {
			t.Fatalf("INSERT with dangling FK reference under NOT ENFORCED should succeed, got: %v", err)
		}
	})

	t.Run("TableLevelNotEnforced", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE cttlnenf_parent (id integer)`); err != nil {
			t.Fatalf("CREATE TABLE cttlnenf_parent: %v", err)
		}
		if err := runDDL(t, ctx, `CREATE TABLE cttlnenf_child (id integer, pid integer, FOREIGN KEY (pid) REFERENCES cttlnenf_parent(id) NOT ENFORCED)`); err != nil {
			t.Fatalf("CREATE TABLE cttlnenf_child: %v", err)
		}

		tbl, ok := cat.LookupTable(parser.ObjectName{Name: "cttlnenf_child"})
		if !ok {
			t.Fatal("cttlnenf_child table not found")
		}
		if len(tbl.ForeignKeys) != 1 || !tbl.ForeignKeys[0].NotEnforced {
			t.Fatalf("expected 1 NotEnforced foreign key, got %+v", tbl.ForeignKeys)
		}

		// Runtime skip: a dangling reference must be allowed under NOT ENFORCED.
		if err := runDDL(t, ctx, `INSERT INTO cttlnenf_child VALUES (1, 999)`); err != nil {
			t.Fatalf("INSERT with dangling FK reference under NOT ENFORCED should succeed, got: %v", err)
		}
	})

	t.Run("TableLevelEnforcedControlRejectsDanglingReference", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE cttlenfc_parent (id integer)`); err != nil {
			t.Fatalf("CREATE TABLE cttlenfc_parent: %v", err)
		}
		if err := runDDL(t, ctx, `CREATE TABLE cttlenfc_child (id integer, pid integer, FOREIGN KEY (pid) REFERENCES cttlenfc_parent(id))`); err != nil {
			t.Fatalf("CREATE TABLE cttlenfc_child: %v", err)
		}
		err := runDDL(t, ctx, `INSERT INTO cttlenfc_child VALUES (1, 999)`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "23503" {
			t.Fatalf("expected 23503 foreign_key_violation on the enforced control, got: %v", err)
		}
	})
}
