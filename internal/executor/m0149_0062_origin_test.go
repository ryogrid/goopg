package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestSubscriptionOriginOption pins M0149-0062a against PG 18.3's
// CreateSubscription / AlterSubscription: WITH (origin = none) is stored in
// pg_subscription.suborigin (it was parsed and dropped — suborigin always
// read "any"), ALTER ... SET (origin = any) resets it, and an unknown value
// is 22023.
func TestSubscriptionOriginOption(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	ctx.PubSub = catalog.NewPubSub()
	for _, q := range []string{
		"CREATE SUBSCRIPTION so_none CONNECTION 'host=remote dbname=app' PUBLICATION p1 WITH (connect = false, origin = none)",
		"CREATE SUBSCRIPTION so_any CONNECTION 'host=remote dbname=app' PUBLICATION p1 WITH (connect = false)",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// suborigin as the pg_subscription row (heap and view) carries it.
	origins := func() string {
		var out []string
		for _, n := range []string{"so_any", "so_none"} {
			sub, ok := ctx.PubSub.LookupSubscription(n)
			if !ok {
				t.Fatalf("subscription %s not registered", n)
			}
			row := buildPGSubscriptionRow(sub)
			out = append(out, n+"|"+row[len(row)-1].StringValue())
		}
		return strings.Join(out, ";")
	}
	if got := origins(); got != "so_any|any;so_none|none" {
		t.Errorf("after CREATE: %q", got)
	}
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION so_none SET (origin = any)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION so_any SET (origin = NONE)"); err != nil {
		t.Fatal(err)
	}
	if got := origins(); got != "so_any|none;so_none|any" {
		t.Errorf("after ALTER: %q", got)
	}
	err := runDDL(t, ctx, "ALTER SUBSCRIPTION so_any SET (origin = local)")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "22023" {
		t.Errorf("origin = local: err = %v, want 22023", err)
	}
}

// TestPartitionTreeCorrelatedAndLateralUnderJoin pins M0149-0062a:
// pg_partition_tree / pg_partition_ancestors take a correlated argument
// inside a sublink and a lateral one in a leaf of an explicit JOIN — the
// shapes of the query check_publications_origin sends a publisher
// (pg_partition_ancestors(PS.srrelid) inside the JOIN's ON clause, and
// `pg_publication P, LATERAL pg_get_publication_tables(P.pubname) GPT JOIN
// …`). Both failed with "column ... does not exist" / "on nil slot".
func TestPartitionTreeCorrelatedAndLateralUnderJoin(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE ptc (a int) PARTITION BY LIST (a)",
		"CREATE TABLE ptc1 PARTITION OF ptc FOR VALUES IN (1)",
		"CREATE TABLE ptc_other (y int)",
		"INSERT INTO ptc_other VALUES (1), (2)",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, tc := range []struct{ q, want string }{
		{"SELECT c.relname, (SELECT count(*) FROM pg_partition_tree(c.oid)), (SELECT count(*) FROM pg_partition_ancestors(c.oid)) FROM pg_class c WHERE c.relname IN ('ptc', 'ptc1') ORDER BY 1",
			"ptc|2|1;ptc1|1|2"},
		{"SELECT c.relname, g.relid::text, o.y FROM pg_class c, LATERAL pg_partition_ancestors(c.oid) g JOIN ptc_other o ON true WHERE c.relname = 'ptc1' ORDER BY 2, 3",
			"ptc1|ptc|1;ptc1|ptc|2;ptc1|ptc1|1;ptc1|ptc1|2"},
		{"SELECT c.relname FROM pg_class c JOIN pg_class d ON (d.oid = c.oid OR d.oid IN (SELECT relid FROM pg_partition_ancestors(c.oid))) WHERE d.relname = 'ptc' ORDER BY 1",
			"ptc;ptc1"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, tc.q)), ";"); got != tc.want {
			t.Errorf("%s\n got %q\nwant %q", tc.q, got, tc.want)
		}
	}
}

// TestApplyUpdateLinksCtidChain pins M0149-0062a: the apply worker's
// UPDATE (simple_table_tuple_update, a heap_update) leaves the old
// version's t_ctid pointing at the new one, as updateOp's non-HOT path
// does. It used to delete and re-insert, so a concurrent READ COMMITTED
// DELETE that waited on the apply transaction found a deleted row with no
// successor and skipped the new version (030_origin.pl: node B's
// "DELETE FROM tab" left the row the apply worker had just updated).
func TestApplyUpdateLinksCtidChain(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE auc (a int PRIMARY KEY)")
	runSQL(t, ctx, "INSERT INTO auc VALUES (32)")
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "auc"})
	if !ok {
		t.Fatal("auc not found")
	}
	// The fixture's own transaction (runSQL runs in it) sees the row.
	ctx.CommandCounterIncrement()
	ctx.CmdID = ctx.GetCurrentCommandId(true)
	rel := cat.RelFileNode(tbl)
	found, err := applyFindTuple(ctx, rel, tbl, tbl.Columns, Row{NewIntDatum(32)}, nil)
	if err != nil || found == nil {
		t.Fatalf("applyFindTuple = %v, %v", found, err)
	}
	if err := applyUpdateFound(ctx, rel, tbl, tbl.Columns, found, Row{NewIntDatum(33)}); err != nil {
		t.Fatal(err)
	}
	sp, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: found.ptr.Block})
	if err != nil {
		t.Fatal(err)
	}
	sp.RLock()
	old, gerr := storage.PageGetHeapTuple(sp.Page(), found.ptr.Offset)
	sp.RUnlock()
	ctx.Pool.Unpin(sp)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if isChainTailCTID(old.Header.CTID, found.ptr.Block, found.ptr.Offset) {
		t.Errorf("old version's t_ctid = %v: still a chain tail, no link to the new version", old.Header.CTID)
	}
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT a FROM auc")), ";"); got != "33" {
		t.Errorf("rows after the apply update = %q, want 33", got)
	}
}
