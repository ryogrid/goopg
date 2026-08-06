package planner

// tagViewOwnerScans marks every base-table scan reachable inside a view's
// inlined plan tree with the checking role the executor should use for its
// SELECT-privilege check: the view owner, mirroring PostgreSQL's default
// (non-security_invoker) view semantics, where a view's underlying-table
// reads run as the view owner rather than the querying session. Without
// this, `GRANT SELECT ON view TO role` alone (no matching grant on the
// base table) is wrongly denied — the executor's scan-level SELECT check
// (M0097-0040) otherwise only ever sees the querying role. M0122-0008.
//
// Only scans whose PrivilegeCheckRoleSet is still false get tagged. A
// nested view (view-of-view) tags its own immediate underlying scans with
// ITS OWN owner during its own recursive Plan() call, which — via the
// depth-first Plan() recursion tagViewOwnerScans's caller relies on —
// completes before the outer view's frame regains control and calls
// tagViewOwnerScans again; leaving already-tagged scans alone preserves
// each nesting level's own owner instead of collapsing them all to the
// outermost view's.
func tagViewOwnerScans(n Node, owner string) {
	switch p := n.(type) {
	case *SeqScan:
		if !p.PrivilegeCheckRoleSet {
			p.PrivilegeCheckRole, p.PrivilegeCheckRoleSet = owner, true
		}
	case *IndexScan:
		if !p.PrivilegeCheckRoleSet {
			p.PrivilegeCheckRole, p.PrivilegeCheckRoleSet = owner, true
		}
	case *IndexOnlyScan:
		if !p.PrivilegeCheckRoleSet {
			p.PrivilegeCheckRole, p.PrivilegeCheckRoleSet = owner, true
		}
	case *Project:
		tagViewOwnerScans(p.Child, owner)
	case *Filter:
		tagViewOwnerScans(p.Child, owner)
	case *Sort:
		tagViewOwnerScans(p.Child, owner)
	case *Limit:
		tagViewOwnerScans(p.Child, owner)
	case *Distinct:
		tagViewOwnerScans(p.Child, owner)
	case *DistinctOn:
		tagViewOwnerScans(p.Child, owner)
	case *Aggregate:
		tagViewOwnerScans(p.Child, owner)
	case *WindowAgg:
		tagViewOwnerScans(p.Child, owner)
	case *ProjectSet:
		tagViewOwnerScans(p.Child, owner)
	case *CTEScan:
		tagViewOwnerScans(p.Child, owner)
	case *LockRows:
		tagViewOwnerScans(p.Child, owner)
	case *Join:
		tagViewOwnerScans(p.Left, owner)
		tagViewOwnerScans(p.Right, owner)
	case *NestedLoopIndexJoin:
		tagViewOwnerScans(p.Outer, owner)
		tagViewOwnerScans(p.Inner, owner)
	case *SetOp:
		tagViewOwnerScans(p.Left, owner)
		tagViewOwnerScans(p.Right, owner)
	}
}
