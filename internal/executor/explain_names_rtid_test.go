package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// A-01(ii) cut 3 — PG-rule unit tests for the RTID-keyed explainNames
// migration (DESIGN.md §5, §8 cut 3): registration keys are the
// planner-stamped statement-unique RTIDs, suffixes follow PostgreSQL's
// set_rtable_names high-water rule (first bare, do/while re-check,
// generated names entered as bases).
//
// Hermetic by construction: hand-built scan nodes with explicit RTIDs run
// through newExplainNames, so no planner shape is assumed and the RTID
// allocation order under test is spelled out literally.

// rtidScan builds a schemaless SeqScan over tblName with the given alias
// and RTID. Schemaless is enough: registration reads base name + RTID,
// and labels read the node pointer — only column() needs output schemas,
// which the literal-based translation test below supplies directly.
func rtidScan(tblName, alias string, rtid int32) *optimizer.SeqScan {
	return &optimizer.SeqScan{
		Table: &catalog.Table{Name: tblName},
		Alias: alias,
		RTID:  rtid,
	}
}

// TestExplainNamesRTIDAliasFirst pins the base-name rule: the FROM-clause
// alias wins over the relation name (PG get_rel_name vs eref->aliasname —
// alias first).
func TestExplainNamesRTIDAliasFirst(t *testing.T) {
	nm := newExplainNames(rtidScan("eq_r", "a", 7))

	if got := nm.bySource[7]; got != "a" {
		t.Errorf("bySource[7] = %q, want %q (alias first)", got, "a")
	}
	if nm.qualify() {
		t.Errorf("single-relation table must not qualify")
	}
}

// TestExplainNamesRTIDFirstBareSecondSuffixed pins first-bare: two scans of
// one relation with no alias print bare, then _1 — for column qualification
// and node labels together.
func TestExplainNamesRTIDFirstBareSecondSuffixed(t *testing.T) {
	a := rtidScan("eq_r", "", 4)
	b := rtidScan("eq_r", "", 9)
	nm := newExplainNames(&optimizer.Join{Left: a, Right: b})

	if got := nm.bySource[4]; got != "eq_r" {
		t.Errorf("bySource[4] = %q, want bare %q", got, "eq_r")
	}
	if got := nm.bySource[9]; got != "eq_r_1" {
		t.Errorf("bySource[9] = %q, want %q", got, "eq_r_1")
	}
	if got := nm.disambiguatedName(a); got != "" {
		t.Errorf("first label = %q, want bare (no entry)", got)
	}
	if got := nm.disambiguatedName(b); got != "eq_r_1" {
		t.Errorf("second label = %q, want %q", got, "eq_r_1")
	}
	if !nm.qualify() {
		t.Errorf("two-relation table must qualify")
	}
}

// TestExplainNamesRTIDLiteralCollisionPushesSuffix pins the high-water
// re-check: a literal alias `x_1` occupies the name the second `x` would
// naively take, so PG hands it `x_2` instead of a second `x_1`.
func TestExplainNamesRTIDLiteralCollisionPushesSuffix(t *testing.T) {
	x1 := rtidScan("eq_r", "x", 1)
	lit := rtidScan("eq_r", "x_1", 2)
	x2 := rtidScan("eq_r", "x", 3)
	nm := newExplainNames(&optimizer.Join{
		Left:  &optimizer.Join{Left: x1, Right: lit},
		Right: x2,
	})

	want := map[int32]string{1: "x", 2: "x_1", 3: "x_2"}
	for rtid, w := range want {
		if got := nm.bySource[rtid]; got != w {
			t.Errorf("bySource[%d] = %q, want %q", rtid, got, w)
		}
	}
	if got := nm.disambiguatedName(x1); got != "" {
		t.Errorf("first x label = %q, want bare", got)
	}
	if got := nm.disambiguatedName(lit); got != "" {
		t.Errorf("literal x_1 label = %q, want bare", got)
	}
	if got := nm.disambiguatedName(x2); got != "x_2" {
		t.Errorf("second x label = %q, want %q", got, "x_2")
	}
}

// TestExplainNamesRTIDSemiJoinLabelSplit pins the SEMI-join same-relation
// case from the nodeLabels comment: both sides carry one SourceTableIdx,
// but each side has its own RTID, so the labels still split bare / _1.
func TestExplainNamesRTIDSemiJoinLabelSplit(t *testing.T) {
	outer := rtidScan("t", "", 1)
	inner := rtidScan("t", "", 2)
	nm := newExplainNames(&optimizer.Join{
		Type:  optimizer.JoinTypeSemi,
		Left:  outer,
		Right: inner,
	})

	if got := nm.bySource[1]; got != "t" {
		t.Errorf("outer bySource = %q, want bare %q", got, "t")
	}
	if got := nm.bySource[2]; got != "t_1" {
		t.Errorf("inner bySource = %q, want %q", got, "t_1")
	}
	if got := nm.disambiguatedName(outer); got != "" {
		t.Errorf("outer label = %q, want bare", got)
	}
	if got := nm.disambiguatedName(inner); got != "t_1" {
		t.Errorf("inner label = %q, want %q", got, "t_1")
	}
}

// TestExplainNamesRTIDOrderBeatsWalkOrder pins the re-keying itself:
// registration follows RTID (allocation ≈ FROM order), not tree position.
// The walk reaches RTID 5 first, but the bare name goes to RTID 3.
func TestExplainNamesRTIDOrderBeatsWalkOrder(t *testing.T) {
	early := rtidScan("t", "", 5)
	late := rtidScan("t", "", 3)
	nm := newExplainNames(&optimizer.Join{Left: early, Right: late})

	if got := nm.bySource[3]; got != "t" {
		t.Errorf("bySource[3] = %q, want bare %q (allocation order)", got, "t")
	}
	if got := nm.bySource[5]; got != "t_1" {
		t.Errorf("bySource[5] = %q, want %q", got, "t_1")
	}
	if got := nm.disambiguatedName(late); got != "" {
		t.Errorf("RTID-3 label = %q, want bare", got)
	}
	if got := nm.disambiguatedName(early); got != "t_1" {
		t.Errorf("RTID-5 label = %q, want %q", got, "t_1")
	}
}

// TestExplainNamesRTIDColumnTranslation pins the storage-type fallback:
// a ColumnRef arrives carrying only its per-level SourceTableIdx, and bySrc
// translates it to the first RTID registered for it — with the cols guard
// still degrading unknown columns and unmapped bindings to bare.
func TestExplainNamesRTIDColumnTranslation(t *testing.T) {
	nm := &explainNames{
		bySource: map[int32]string{7: "a"},
		bySrc:    map[int16]int32{3: 7},
		cols:     map[int32]map[string]bool{7: {"id": true}},
	}

	if got := nm.column(3, "id", true); got != "a.id" {
		t.Errorf("column(3, id) = %q, want %q", got, "a.id")
	}
	if got := nm.column(3, "st", true); got != "st" {
		t.Errorf("column(3, st) = %q, want bare (cols guard)", got)
	}
	if got := nm.column(9, "id", true); got != "id" {
		t.Errorf("column(9, id) = %q, want bare (unmapped src)", got)
	}
	if got := nm.column(0, "id", true); got != "id" {
		t.Errorf("column(0, id) = %q, want bare (src 0)", got)
	}
	if got := nm.column(3, "id", false); got != "id" {
		t.Errorf("unprefixed column(3, id) = %q, want bare", got)
	}
}

// TestExplainNamesRTIDBitmapHeapScan covers the cut-3 BitmapHeapScan switch
// extension (§4: stamping the field without extending explainRelBaseName and
// explainIsScanNode would stamp a field nobody reads). BitmapIndexScan stays
// excluded — it renders as Recheck-Cond machinery, never as a named
// relation.
func TestExplainNamesRTIDBitmapHeapScan(t *testing.T) {
	tbl := &catalog.Table{Name: "customer", Schema: "public"}
	bhs := &optimizer.BitmapHeapScan{Table: tbl, Alias: "c2", RTID: 5}
	nm := newExplainNames(bhs)

	if got := nm.bySource[5]; got != "c2" {
		t.Errorf("bySource[5] = %q, want alias %q", got, "c2")
	}
	if !explainIsScanNode(bhs) {
		t.Errorf("explainIsScanNode(BitmapHeapScan) = false, want true")
	}
	if explainIsScanNode(&optimizer.BitmapIndexScan{Table: tbl}) {
		t.Errorf("explainIsScanNode(BitmapIndexScan) = true, want false (no relation identity)")
	}

	bare := &optimizer.BitmapHeapScan{Table: tbl, RTID: 6}
	bm := newExplainNames(bare)
	if got := bm.bySource[6]; got != "customer" {
		t.Errorf("bare bySource[6] = %q, want %q", got, "customer")
	}
}
