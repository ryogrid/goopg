package catalog

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCatalogCreateLookupDrop pins the round-trip: a created table is
// found by both LookupTable and LookupColumn (case-insensitive), and
// can be dropped.
func TestCatalogCreateLookupDrop(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "pgbench_accounts"}, []Column{
		{Name: "aid", Type: Type{Name: "int4"}, NotNull: true},
		{Name: "abalance", Type: Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tbl.OID < FirstUserOID {
		t.Errorf("OID=%d want >= %d", tbl.OID, FirstUserOID)
	}
	if tbl.Columns[0].Ordinal != 0 || tbl.Columns[1].Ordinal != 1 {
		t.Errorf("ordinals not assigned")
	}

	got, ok := c.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	if !ok || got.OID != tbl.OID {
		t.Fatalf("LookupTable round-trip failed: ok=%v", ok)
	}
	col, ok := c.LookupColumn(got, "ABALANCE")
	if !ok || col.Ordinal != 1 {
		t.Fatalf("LookupColumn case-insensitive failed: ok=%v col=%+v", ok, col)
	}

	rfn := c.RelFileNode(got)
	if rfn.RelOid != tbl.OID || rfn.DBOid != DefaultDBOid {
		t.Errorf("RelFileNode=%+v", rfn)
	}

	if err := c.DropTable(parser.ObjectName{Name: "pgbench_accounts"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.LookupTable(parser.ObjectName{Name: "pgbench_accounts"}); ok {
		t.Errorf("table should be gone after DropTable")
	}
}

// TestCatalogDuplicateAndMissing locks down the error paths.
func TestCatalogDuplicateAndMissing(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, nil); err == nil {
		t.Error("duplicate CreateTable should fail")
	}
	if err := c.DropTable(parser.ObjectName{Name: "missing"}); err == nil {
		t.Error("DropTable of missing should fail")
	}
}

func TestCatalogIndexLifecycle(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "items"}, []Column{{Name: "id", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "items_id_idx"}, tbl, []string{"id"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	if idx.OID <= tbl.OID {
		t.Fatalf("index oid=%d should be greater than table oid=%d", idx.OID, tbl.OID)
	}
	if _, ok := c.LookupIndex(parser.ObjectName{Name: "items_id_idx"}); !ok {
		t.Fatal("LookupIndex failed")
	}
	idxs := c.IndexesOnTable(tbl)
	if len(idxs) != 1 || idxs[0].Name != "items_id_idx" {
		t.Fatalf("IndexesOnTable=%+v", idxs)
	}
	rfn := c.IndexRelFileNode(idx)
	if rfn.RelOid != idx.OID || rfn.DBOid != DefaultDBOid {
		t.Fatalf("IndexRelFileNode=%+v", rfn)
	}
	if err := c.DropIndex(parser.ObjectName{Name: "items_id_idx"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.LookupIndex(parser.ObjectName{Name: "items_id_idx"}); ok {
		t.Fatal("index should be gone after DropIndex")
	}
}

func TestCatalogDropTableAlsoDropsIndexes(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, []Column{{Name: "id", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "t_pkey"}, tbl, []string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	if !c.HasPrimaryKey(tbl) {
		t.Fatal("expected HasPrimaryKey to be true")
	}
	if err := c.DropTable(parser.ObjectName{Name: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.LookupIndex(parser.ObjectName{Name: "t_pkey"}); ok {
		t.Fatal("index metadata should be removed when table is dropped")
	}
}

func TestCatalogAddColumn(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "items"}, []Column{{Name: "id", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	col, err := c.AddColumn(tbl, Column{Name: "label", Type: Type{Name: "text"}})
	if err != nil {
		t.Fatal(err)
	}
	if col.Ordinal != 1 {
		t.Fatalf("new column ordinal=%d want=1", col.Ordinal)
	}
	if _, err := c.AddColumn(tbl, Column{Name: "LABEL", Type: Type{Name: "text"}}); err == nil {
		t.Fatal("duplicate AddColumn should fail")
	}
}

// TestPgCatalogBootstrapViews pins the pg_database / pg_roles /
// pg_tables virtual views HammerDB queries during bootstrap +
// checkschema. Each is exposed under pg_catalog and resolves
// unqualified via the search_path fallback.
func TestPgCatalogBootstrapViews(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "items"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"pg_database", "pg_roles", "pg_tables"} {
		v, ok := c.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("LookupTable(%s) failed — search_path fallback not honored", name)
		}
		if !v.Virtual || v.VirtualRows == nil {
			t.Fatalf("%s is not a virtual view", name)
		}
		rows := v.VirtualRows()
		if len(rows) == 0 {
			t.Errorf("%s: empty rows", name)
		}
	}

	tables, _ := c.LookupTable(parser.ObjectName{Name: "pg_tables"})
	got := tables.VirtualRows()
	if len(got) != 1 || got[0][1] != "items" {
		t.Errorf("pg_tables rows=%v want one (public, items, postgres)", got)
	}
}

// TestPgClassExposesRelNatts pins the M0103-0008 rung-14 surface:
// PG's CREATE SUBSCRIPTION column-list probe runs
//
//	… WHEN (array_length(gpt.attrs,1) = c.relnatts) … FROM pg_class c
//
// against the publisher. Before rung 14 goopg's pg_class virtual
// view omitted `relnatts`, so the probe failed with SQLSTATE 42703
// ("column \"relnatts\" does not exist") and CREATE SUBSCRIPTION
// registered zero relations in pg_subscription_rel — every change
// then silently skipped on the subscriber.
//
// Pin: relnatts is present at ordinal 8, typed int4, and populated
// with the table's user-column count (no system columns — goopg
// has no rowid/ctid in its catalog).
func TestPgClassExposesRelNatts(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
		{Name: "v", Type: Type{Name: "text"}},
	}); err != nil {
		t.Fatal(err)
	}

	pgClass, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok {
		t.Fatal("pg_catalog.pg_class missing")
	}

	var natts *Column
	for i := range pgClass.Columns {
		if pgClass.Columns[i].Name == "relnatts" {
			natts = &pgClass.Columns[i]
			break
		}
	}
	if natts == nil {
		t.Fatal("pg_class.relnatts column not declared")
	}
	if natts.Type.Name != "int4" {
		t.Errorf("pg_class.relnatts type=%q want int4", natts.Type.Name)
	}

	rows := pgClass.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("pg_class rows=%d want 1 (the user 't' table)", len(rows))
	}
	row := rows[0]
	if len(row) != len(pgClass.Columns) {
		t.Fatalf("pg_class row width=%d want %d (one cell per column)", len(row), len(pgClass.Columns))
	}
	if row[natts.Ordinal] != "2" {
		t.Errorf("pg_class.t.relnatts=%q want %q (user column count)", row[natts.Ordinal], "2")
	}
}

// TestPgClassExposesRelReplident pins rung 16 of M0103-0008: the
// pg_catalog.pg_class virtual view must expose a `relreplident`
// column populated as 'd' (REPLICA_IDENTITY_DEFAULT). CREATE
// SUBSCRIPTION's `fetch_remote_table_info` first probe selects
// `c.oid, c.relreplident, c.relkind`; without this column the
// query failed with 42703 ("column does not exist") before any
// further publisher-side correctness work could be exercised.
func TestPgClassExposesRelReplident(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}

	pgClass, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok {
		t.Fatal("pg_catalog.pg_class missing")
	}

	var ri *Column
	for i := range pgClass.Columns {
		if pgClass.Columns[i].Name == "relreplident" {
			ri = &pgClass.Columns[i]
			break
		}
	}
	if ri == nil {
		t.Fatal("pg_class.relreplident column not declared")
	}
	if ri.Type.Name != "char" {
		t.Errorf("pg_class.relreplident type=%q want char", ri.Type.Name)
	}

	rows := pgClass.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("pg_class rows=%d want 1", len(rows))
	}
	if rows[0][ri.Ordinal] != "d" {
		t.Errorf("pg_class.t.relreplident=%q want %q (REPLICA_IDENTITY_DEFAULT)", rows[0][ri.Ordinal], "d")
	}
}

// TestPgClassOidIsNumericOID pins rung 16 of M0103-0008: pg_class.oid
// must emit the table's numeric OID (as decimal text) so libpqrcv can
// decode it via DatumGetObjectId. Pre-rung-16 the column stored the
// relation name; PG's `lrel->remoteid = DatumGetObjectId(c.oid)` then
// parsed "t" as uint32 → 0 and every downstream column-list LATERAL
// probe matched zero rows, leaving the apply worker dormant.
func TestPgClassOidIsNumericOID(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	pgClass, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok {
		t.Fatal("pg_catalog.pg_class missing")
	}

	var oidCol *Column
	for i := range pgClass.Columns {
		if pgClass.Columns[i].Name == "oid" {
			oidCol = &pgClass.Columns[i]
			break
		}
	}
	if oidCol == nil {
		t.Fatal("pg_class.oid column not declared")
	}
	if oidCol.Type.Name != "oid" {
		t.Errorf("pg_class.oid type=%q want oid", oidCol.Type.Name)
	}

	rows := pgClass.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("pg_class rows=%d want 1", len(rows))
	}
	want := strconv.Itoa(int(tbl.OID))
	if got := rows[0][oidCol.Ordinal]; got != want {
		t.Errorf("pg_class.t.oid=%q want %q (numeric OID, M0103-0008 rung 16)", got, want)
	}
}

// TestPgIndexesView pins the pg_catalog.pg_indexes virtual
// view that HammerDB's checkschema queries. Each index on a
// non-virtual table should produce one row with
// (schemaname, tablename, indexname, ...). Unqualified lookup
// resolves via the implicit pg_catalog search_path.
func TestPgIndexesView(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "items"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
		{Name: "label", Type: Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "items_id_idx"}, tbl, []string{"id"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	view, ok := c.LookupTable(parser.ObjectName{Name: "pg_indexes"})
	if !ok {
		t.Fatal("LookupTable(pg_indexes) failed — search_path fallback to pg_catalog not honored")
	}
	if !view.Virtual || view.VirtualRows == nil {
		t.Fatalf("pg_indexes is not a virtual view: virtual=%v rows=nil(%t)", view.Virtual, view.VirtualRows == nil)
	}
	rows := view.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if got, want := rows[0][1], "items"; got != want {
		t.Errorf("tablename=%q want %q", got, want)
	}
	if got, want := rows[0][2], "items_id_idx"; got != want {
		t.Errorf("indexname=%q want %q", got, want)
	}
}

// TestSystemCatalogOIDConstants verifies the fixed OIDs match upstream's
// values so ODBC/JDBC metadata probes that look up by numeric OID see the
// expected numbers.
func TestSystemCatalogOIDConstants(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"TypeRelationId (pg_type)", TypeRelationId, 1247},
		{"AttributeRelationId (pg_attribute)", AttributeRelationId, 1249},
		{"RelationRelationId (pg_class)", RelationRelationId, 1259},
		{"FirstUserOID", FirstUserOID, 16384},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestIsSystemRelation checks the OID range boundary.
func TestIsSystemRelation(t *testing.T) {
	cases := []struct {
		oid  uint32
		want bool
	}{
		{TypeRelationId, true},
		{AttributeRelationId, true},
		{RelationRelationId, true},
		{FirstUserOID - 1, true},
		{FirstUserOID, false},
		{FirstUserOID + 1, false},
		{0xFFFFFFFF, false},
	}
	for _, tc := range cases {
		if got := IsSystemRelation(tc.oid); got != tc.want {
			t.Errorf("IsSystemRelation(%d) = %v, want %v", tc.oid, got, tc.want)
		}
	}
}

// TestSystemRelationOIDsBelowFirstUserOID is a cross-check: all three
// fixed catalog OIDs must be recognised as system relations.
func TestSystemRelationOIDsBelowFirstUserOID(t *testing.T) {
	for _, oid := range []uint32{TypeRelationId, AttributeRelationId, RelationRelationId} {
		if !IsSystemRelation(oid) {
			t.Errorf("OID %d should be a system relation (< FirstUserOID %d)", oid, FirstUserOID)
		}
	}
}

// TestNextOIDAndAdvanceNextOIDPast verifies the M0106-0013 helpers:
// NextOID() reads the current counter; AdvanceNextOIDPast(oid) ensures
// the counter is strictly above oid.
func TestNextOIDAndAdvanceNextOIDPast(t *testing.T) {
	c := NewInMemory()
	initial := c.NextOID()
	if initial < FirstUserOID {
		t.Fatalf("initial nextOID %d < FirstUserOID %d", initial, FirstUserOID)
	}

	// Advance to a value already below the counter — must be a no-op.
	c.AdvanceNextOIDPast(initial - 1)
	if got := c.NextOID(); got != initial {
		t.Errorf("advance below current: got %d want %d (no-op)", got, initial)
	}

	// Advance to a value above the counter — must set counter to oid+1.
	above := initial + 500
	c.AdvanceNextOIDPast(above)
	if got := c.NextOID(); got != above+1 {
		t.Errorf("advance above current: got %d want %d", got, above+1)
	}

	// Advancing to current counter value − 1 again must still be a no-op.
	c.AdvanceNextOIDPast(above - 1)
	if got := c.NextOID(); got != above+1 {
		t.Errorf("advance below (after larger advance): got %d want %d", got, above+1)
	}
}

// TestPgSettingsEnableGUCsCompleteAndSorted pins the pg_settings virtual
// table's enable_* coverage and ordering. The sysviews regress test runs
// `select name, setting from pg_settings where name like 'enable%'` WITHOUT
// an ORDER BY and expects PostgreSQL 18's 24 alphabetically-sorted planner
// enable_* GUCs. PostgreSQL's pg_settings is backed by the sorted GUC table,
// so the virtual rows must (a) cover the exact set and (b) be name-sorted.
// Regression guard for the M0097-0032 fix (was 20 GUCs, registration order,
// with the mis-named enable_gather_merge instead of enable_gathermerge).
func TestPgSettingsEnableGUCsCompleteAndSorted(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_settings"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_settings virtual table not registered")
	}
	want := []string{
		"enable_async_append", "enable_bitmapscan", "enable_distinct_reordering",
		"enable_gathermerge", "enable_group_by_reordering", "enable_hashagg",
		"enable_hashjoin", "enable_incremental_sort", "enable_indexonlyscan",
		"enable_indexscan", "enable_material", "enable_memoize", "enable_mergejoin",
		"enable_nestloop", "enable_parallel_append", "enable_parallel_hash",
		"enable_partition_pruning", "enable_partitionwise_aggregate",
		"enable_partitionwise_join", "enable_presorted_aggregate",
		"enable_self_join_elimination", "enable_seqscan", "enable_sort",
		"enable_tidscan",
	}

	var got []string
	var prevName string
	for _, row := range tbl.VirtualRows() {
		name := row[0]
		// Overall name-sort contract (mirrors PG's sorted GUC table).
		if prevName != "" && name < prevName {
			t.Errorf("pg_settings rows not name-sorted: %q precedes %q", prevName, name)
		}
		prevName = name
		if len(name) >= 7 && name[:7] == "enable_" {
			got = append(got, name)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("enable_* GUC count = %d, want %d\n got: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("enable_* GUC[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestVerboseIntervalOffset pins the postgres_verbose interval rendering used
// by the timezone system views. pg_regress forces intervalstyle=
// postgres_verbose, so the LMT row must read "@ 7 hours 52 mins 58 secs ago".
func TestVerboseIntervalOffset(t *testing.T) {
	cases := []struct {
		secs int
		want string
	}{
		{0, "@ 0"},
		{3600, "@ 1 hour"},
		{2 * 3600, "@ 2 hours"},
		{-3600, "@ 1 hour ago"},
		{5*3600 + 30*60, "@ 5 hours 30 mins"},
		{-(9*3600 + 30*60), "@ 9 hours 30 mins ago"},
		{-(7*3600 + 52*60 + 58), "@ 7 hours 52 mins 58 secs ago"},
	}
	for _, tc := range cases {
		if got := verboseIntervalOffset(tc.secs); got != tc.want {
			t.Errorf("verboseIntervalOffset(%d) = %q, want %q", tc.secs, got, tc.want)
		}
	}
}

// TestPgTimezoneAbbrevsLMTRow guards the sysviews regress expectation:
// `select * from pg_timezone_abbrevs where abbrev = 'LMT'` must return the
// verbose-interval offset and a "f" is_dst (not "false"/"-07:52:58").
func TestPgTimezoneAbbrevsLMTRow(t *testing.T) {
	c := NewInMemory()
	for _, name := range []string{"pg_timezone_abbrevs", "pg_timezone_names"} {
		tbl, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: name})
		if !ok || tbl.VirtualRows == nil {
			t.Fatalf("%s virtual table not registered", name)
		}
		var found bool
		for _, row := range tbl.VirtualRows() {
			// abbrev is col 0 for abbrevs, col 1 for names.
			abbrevIdx := 0
			isDstIdx := len(row) - 1
			offIdx := isDstIdx - 1
			if row[abbrevIdx] != "LMT" && row[len(row)-3] != "LMT" {
				continue
			}
			// Locate the LMT row regardless of table shape.
			if !(row[0] == "LMT" || (len(row) >= 3 && row[len(row)-3] == "LMT")) {
				continue
			}
			found = true
			if row[offIdx] != "@ 7 hours 52 mins 58 secs ago" {
				t.Errorf("%s LMT utc_offset = %q, want verbose interval", name, row[offIdx])
			}
			if row[isDstIdx] != "f" {
				t.Errorf("%s LMT is_dst = %q, want \"f\"", name, row[isDstIdx])
			}
		}
		if !found {
			t.Errorf("%s: no LMT row found", name)
		}
	}
}

// TestPgWaitEventsCoversAllTypes guards the sysviews regress expectation:
// `select type, count(*) > 0 ... from pg_wait_events group by type` must
// return all nine non-InjectionPoint wait-event types PG 18 emits
// (M0097-0032). goopg previously listed only six (missing BufferPin,
// Extension, IPC), so the GROUP BY came up short.
func TestPgWaitEventsCoversAllTypes(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_wait_events"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_wait_events virtual table not registered")
	}
	want := map[string]bool{
		"Activity": false, "BufferPin": false, "Client": false,
		"Extension": false, "IO": false, "IPC": false,
		"LWLock": false, "Lock": false, "Timeout": false,
	}
	for _, row := range tbl.VirtualRows() {
		typ := row[0]
		if _, known := want[typ]; !known && typ != "InjectionPoint" {
			t.Errorf("unexpected wait-event type %q (not in PG 18 sysviews set)", typ)
		}
		want[typ] = true
	}
	for typ, present := range want {
		if !present {
			t.Errorf("pg_wait_events missing type %q", typ)
		}
	}
}

// TestPgHbaFileRulesErrorIsNull verifies the single canned pg_hba_file_rules
// row leaves its trailing `error` column as SQL NULL. sysviews.sql asserts
// `count(*) FILTER (WHERE error IS NOT NULL) = 0` (no_err = t); an empty
// string would be NOT NULL and fail it. Both the planner and executor
// materialise a missing trailing cell as NullConst, so the row must stop
// before the last (`error`) column.
func TestPgHbaFileRulesErrorIsNull(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_hba_file_rules"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_hba_file_rules virtual table not registered")
	}
	errorCol := -1
	for i, col := range tbl.Columns {
		if col.Name == "error" {
			errorCol = i
		}
	}
	if errorCol != len(tbl.Columns)-1 {
		t.Fatalf("error column at ordinal %d; expected it to be the last column %d "+
			"(NULL-via-truncation relies on this)", errorCol, len(tbl.Columns)-1)
	}
	rows := tbl.VirtualRows()
	if len(rows) == 0 {
		t.Fatal("pg_hba_file_rules must return at least one row (sysviews wants count(*) > 0)")
	}
	for i, row := range rows {
		if len(row) > errorCol {
			t.Errorf("row %d has %d cells, including the error column; the trailing "+
				"error cell must be omitted so it materialises as NULL", i, len(row))
		}
	}
}
