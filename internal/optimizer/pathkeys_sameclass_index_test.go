package optimizer

import "testing"

// TestSameClassIndexMatchesScan pins M0122-0012 slice 42: sameClass finds each
// expression's class through ecClassOf's key / node index instead of scanning
// ecMembers, and must answer exactly what the scan answered — the class of the
// FIRST member, in ecMembers order, that exprEqualKeyed matches. The members
// mix duplicate keys across classes (the first wins), expressions with no
// identity key (a sublink: matched by node only), a nil member, and keyed and
// literal (unkeyed) members.
func TestSameClassIndexMatchesScan(t *testing.T) {
	col := func(i int, n string) *ColumnRef { return &ColumnRef{Index: i, Name: n} }
	a1, a2 := col(1, "a"), col(1, "a") // equal, distinct nodes
	b, c, z := col(2, "b"), col(3, "c"), col(9, "z")
	sub1, sub2 := &SubqueryExpr{Plan: &SeqScan{}}, &SubqueryExpr{Plan: &SeqScan{}} // an inner plan: no identity key under scopeVeto
	if _, ok := exprIdentityKey(sub1, scopeVeto); ok {
		t.Fatal("a sublink unexpectedly has an identity key; pick another keyless expression")
	}
	scan := func(u *pathkeyUsefulness, e Expr) int {
		ke, oke := u.keyOf(e)
		for i := range u.ecMembers {
			m := &u.ecMembers[i]
			mk, mok := u.memberKey(m)
			if exprEqualKeyed(m.expr, mk, mok, e, ke, oke) {
				return m.id
			}
		}
		return -1
	}
	for _, keyed := range []bool{true, false} {
		mk := func(e Expr, id int) classMember {
			if keyed {
				return newClassMember(e, RelSet(1), id)
			}
			return classMember{expr: e, relids: RelSet(1), id: id}
		}
		u := &pathkeyUsefulness{ecMembers: []classMember{
			mk(b, 4), mk(a1, 1), mk(c, 2), mk(a2, 3), mk(sub1, 5), mk(nil, 6), mk(b, 7),
		}}
		probes := []Expr{a1, a2, col(1, "a"), b, c, z, sub1, sub2, nil}
		for _, x := range probes {
			if got, want := u.ecClassOf(x), scan(u, x); got != want {
				t.Fatalf("keyed=%v ecClassOf(%v) = %d, scan = %d", keyed, x, got, want)
			}
			for _, y := range probes {
				want := scan(u, x) >= 0 && scan(u, x) == scan(u, y)
				if got := u.sameClass(x, y); got != want {
					t.Fatalf("keyed=%v sameClass(%v, %v) = %v, scan says %v", keyed, x, y, got, want)
				}
			}
		}
	}
}
