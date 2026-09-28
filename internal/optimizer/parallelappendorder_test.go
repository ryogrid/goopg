package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0146-0005ac — create_append_path's parallel arm sort over goopg's
// left-deep UNION ALL chain (TPC-DS Q71: PG lists store, catalog, web —
// equal startups, descending totals — where goopg kept the written order).
// Each link goes through orderParallelAppendArms as createSetOpPlan builds
// it, innermost first — the inner link's own reorder is what the outer one
// must not re-pair by position (the first live run printed store, web,
// catalog).

func paArm(name string, startup, total float64) (*Values, *Path) {
	return &Values{schema: Schema{{Name: name}}}, &Path{Kind: PathSeqScan, Cost: Cost{Startup: startup, Total: total}}
}

func paLink(l, r Node, lp, rp *Path) (*SetOp, *Path) {
	n := &SetOp{Left: l, Right: r, All: true, Op: parser.SetOpUnion, ParallelAware: true}
	return n, &Path{Kind: PathSetOp, ParallelAware: true, Children: []*Path{lp, rp}}
}

func paFlatten(n Node) []string {
	s, ok := n.(*SetOp)
	if !ok {
		return []string{n.Output()[0].Name}
	}
	return append(paFlatten(s.Left), paFlatten(s.Right)...)
}

func TestParallelAppendArmOrderQ71(t *testing.T) {
	web, wp := paArm("web", 2068.78, 7759.43)
	cat, cpth := paArm("catalog", 2068.78, 12952.54)
	store, sp := paArm("store", 2068.78, 17936.56)
	inner, ip := paLink(web, cat, wp, cpth)
	top, tp := paLink(orderParallelAppendArms(ip, inner), store, ip, sp)

	got := orderParallelAppendArms(tp, top)
	if want, arms := []string{"store", "catalog", "web"}, paFlatten(got); len(arms) != 3 || arms[0] != want[0] || arms[1] != want[1] || arms[2] != want[2] {
		t.Fatalf("arm order = %v, want %v", arms, want)
	}
	// The set operation keeps the written first arm's column names.
	if name := got.Output()[0].Name; name != "web" {
		t.Errorf("output column = %q, want the written first arm's %q", name, "web")
	}
}

func TestParallelAppendArmOrderNonPartialFirstAndStartup(t *testing.T) {
	a, ap := paArm("a", 5, 100)  // partial, lower startup
	b, bp := paArm("b", 9, 50)   // partial, higher startup -> before a
	c, cpth := paArm("c", 0, 10) // claimed whole -> first overall
	inner, ip := paLink(a, b, ap, bp)
	top, tp := paLink(orderParallelAppendArms(ip, inner), c, ip, cpth)
	top.RightNonPartial = true

	got := orderParallelAppendArms(tp, top)
	if arms := paFlatten(got); arms[0] != "c" || arms[1] != "b" || arms[2] != "a" {
		t.Fatalf("arm order = %v, want [c b a]", arms)
	}
	in := got.Left.(*SetOp)
	if !in.LeftNonPartial || in.RightNonPartial || got.RightNonPartial {
		t.Errorf("claimed-whole mark did not follow its arm: inner L=%v R=%v top R=%v", in.LeftNonPartial, in.RightNonPartial, got.RightNonPartial)
	}
}

func TestParallelAppendArmOrderLeavesSerialAndSortedChainsAlone(t *testing.T) {
	x, xp := paArm("x", 0, 30)
	y, yp := paArm("y", 0, 20)
	top, tp := paLink(x, y, xp, yp)
	if got := orderParallelAppendArms(tp, top); got != top {
		t.Errorf("already-sorted chain was rebuilt")
	}
	serial, sp := paLink(y, x, yp, xp)
	serial.ParallelAware = false
	if got := orderParallelAppendArms(sp, serial); got != serial {
		t.Errorf("serial Append was reordered; PG sorts only parallel-aware Appends")
	}
}
