package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanViewInliningTagsScanWithOwnerRole pins the view-owner privilege
// fix (M0122-0008): PostgreSQL runs a view's underlying-table reads as the
// view owner, not the querying session, so `GRANT SELECT ON view TO role`
// alone (no base-table grant) must still let role query through it. This
// requires the planner to tag every scan inside an inlined view's plan
// tree with the view's owner — verified here by planning `SELECT * FROM
// v_acc` (a view over pgbench_accounts) and confirming the resulting
// SeqScan carries PrivilegeCheckRole == the view's owner.
func TestPlanViewInliningTagsScanWithOwnerRole(t *testing.T) {
	cat := pgbenchCatalog(t)
	innerSel := parseOne(t, `SELECT aid FROM pgbench_accounts`).(*parser.SelectStmt)
	vt, err := cat.CreateView(parser.ObjectName{Name: "v_acc"},
		[]catalog.Column{{Name: "aid", Type: catalog.Type{Name: "int4"}}},
		[]string{"aid"}, innerSel, false)
	if err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	vt.Owner = "bob"

	plan, err := Plan(parseOne(t, `SELECT aid FROM v_acc`).(*parser.SelectStmt), cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	scan := findSeqScan(t, plan, "pgbench_accounts")
	if !scan.PrivilegeCheckRoleSet {
		t.Fatalf("expected PrivilegeCheckRoleSet on the inlined view's SeqScan")
	}
	if scan.PrivilegeCheckRole != "bob" {
		t.Fatalf("expected PrivilegeCheckRole == %q, got %q", "bob", scan.PrivilegeCheckRole)
	}
}

// TestPlanViewInliningSecurityInvokerSkipsOwnerTag pins the
// `WITH (security_invoker = true)` opt-out: PostgreSQL 15+ runs such a
// view's underlying reads as the querying role, not the owner, so the
// planner must leave the scan untagged (PrivilegeCheckRoleSet == false).
func TestPlanViewInliningSecurityInvokerSkipsOwnerTag(t *testing.T) {
	cat := pgbenchCatalog(t)
	innerSel := parseOne(t, `SELECT aid FROM pgbench_accounts`).(*parser.SelectStmt)
	vt, err := cat.CreateView(parser.ObjectName{Name: "v_acc_inv"},
		[]catalog.Column{{Name: "aid", Type: catalog.Type{Name: "int4"}}},
		[]string{"aid"}, innerSel, false)
	if err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	vt.Owner = "bob"
	vt.SecurityInvoker = true
	vt.SecurityInvokerSet = true

	plan, err := Plan(parseOne(t, `SELECT aid FROM v_acc_inv`).(*parser.SelectStmt), cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	scan := findSeqScan(t, plan, "pgbench_accounts")
	if scan.PrivilegeCheckRoleSet {
		t.Fatalf("security_invoker view must NOT tag its scans with the owner role, got role %q", scan.PrivilegeCheckRole)
	}
}

// findSeqScan locates the (single, expected) *SeqScan against tableName
// anywhere in the plan tree, failing the test if it isn't found.
func findSeqScan(t *testing.T, n Node, tableName string) *SeqScan {
	t.Helper()
	var found *SeqScan
	var walk func(Node)
	walk = func(n Node) {
		if found != nil || n == nil {
			return
		}
		switch p := n.(type) {
		case *SeqScan:
			if p.Table != nil && p.Table.Name == tableName {
				found = p
			}
		case *Project:
			walk(p.Child)
		case *Filter:
			walk(p.Child)
		}
	}
	walk(n)
	if found == nil {
		t.Fatalf("no SeqScan against %q found in plan", tableName)
	}
	return found
}
