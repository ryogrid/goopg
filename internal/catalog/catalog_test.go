package catalog

import (
	"strconv"
	"strings"
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

	// pg_dump's collectRoleNames runs
	// `SELECT oid, rolname FROM pg_catalog.pg_roles ORDER BY 1`
	// (pg_dump.c:10548), so pg_roles must expose oid as its first
	// column carrying the postgres superuser's OID 10.
	roles, _ := c.LookupTable(parser.ObjectName{Name: "pg_roles"})
	if roles.Columns[0].Name != "oid" || roles.Columns[0].Type.Name != "oid" {
		t.Errorf("pg_roles col0=%+v want {oid oid}", roles.Columns[0])
	}
	if roles.Columns[1].Name != "rolname" {
		t.Errorf("pg_roles col1=%s want rolname", roles.Columns[1].Name)
	}
	rrows := roles.VirtualRows()
	if len(rrows) != 1 || rrows[0][0] != "10" || rrows[0][1] != "postgres" {
		t.Errorf("pg_roles rows=%v want one (10, postgres, t, t)", rrows)
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
	if natts.Type.Name != "int2" {
		t.Errorf("pg_class.relnatts type=%q want int2 (PG18 physical type)", natts.Type.Name)
	}

	rows := pgClass.VirtualRows()
	var row []string
	for _, r := range rows {
		if len(r) > 1 && r[1] == "t" { // relname is column 1
			row = r
			break
		}
	}
	if row == nil {
		t.Fatalf("pg_class has no row for user table 't' (rows=%d)", len(rows))
	}
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
	var row []string
	for _, r := range rows {
		if len(r) > 1 && r[1] == "t" {
			row = r
			break
		}
	}
	if row == nil {
		t.Fatalf("pg_class has no row for user table 't' (rows=%d)", len(rows))
	}
	if row[ri.Ordinal] != "d" {
		t.Errorf("pg_class.t.relreplident=%q want %q (REPLICA_IDENTITY_DEFAULT)", row[ri.Ordinal], "d")
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
	var row []string
	for _, r := range rows {
		if len(r) > 1 && r[1] == "t" {
			row = r
			break
		}
	}
	if row == nil {
		t.Fatalf("pg_class has no row for user table 't' (rows=%d)", len(rows))
	}
	want := strconv.Itoa(int(tbl.OID))
	if got := row[oidCol.Ordinal]; got != want {
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

// TestPgBackendMemoryContextsCallerTuplesRow verifies the "Caller tuples" Bump context row
// exists with the values required by the sysviews regress test:
//
//	type="Bump", total_bytes>0, total_nblocks=2, free_bytes>0, free_chunks=0
func TestPgBackendMemoryContextsCallerTuplesRow(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_backend_memory_contexts"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_backend_memory_contexts virtual table not registered")
	}
	colIdx := map[string]int{}
	for i, col := range tbl.Columns {
		colIdx[col.Name] = i
	}
	rows := tbl.VirtualRows()
	var found bool
	for _, row := range rows {
		if row[colIdx["name"]] != "Caller tuples" {
			continue
		}
		found = true
		if row[colIdx["type"]] != "Bump" {
			t.Errorf("Caller tuples type = %q; want Bump", row[colIdx["type"]])
		}
		if row[colIdx["total_nblocks"]] != "2" {
			t.Errorf("Caller tuples total_nblocks = %q; want 2", row[colIdx["total_nblocks"]])
		}
		if row[colIdx["free_chunks"]] != "0" {
			t.Errorf("Caller tuples free_chunks = %q; want 0", row[colIdx["free_chunks"]])
		}
		if row[colIdx["total_bytes"]] == "0" || row[colIdx["total_bytes"]] == "" {
			t.Errorf("Caller tuples total_bytes = %q; must be > 0", row[colIdx["total_bytes"]])
		}
		if row[colIdx["free_bytes"]] == "0" || row[colIdx["free_bytes"]] == "" {
			t.Errorf("Caller tuples free_bytes = %q; must be > 0", row[colIdx["free_bytes"]])
		}
	}
	if !found {
		t.Fatal("pg_backend_memory_contexts: no 'Caller tuples' row found")
	}
}

// TestPgBackendMemoryContextsPathArrayValues verifies that each row's path column
// is a PG array literal (starts with '{') and that CacheMemoryContext has >= 2
// sibling/child rows sharing the same element at its level index (the
// sysviews CacheMemoryContext-multi-child query).
func TestPgBackendMemoryContextsPathArrayValues(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_backend_memory_contexts"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_backend_memory_contexts not registered")
	}
	colIdx := map[string]int{}
	for i, col := range tbl.Columns {
		colIdx[col.Name] = i
	}
	rows := tbl.VirtualRows()
	// All rows must have a non-empty path that starts with '{'.
	for _, row := range rows {
		p := row[colIdx["path"]]
		if p == "" || p[0] != '{' {
			t.Errorf("row %q: path=%q must be a PG array literal starting with '{'", row[colIdx["name"]], p)
		}
	}
	// Find CacheMemoryContext and its level.
	var cacheLevel int
	var cachePath string
	for _, row := range rows {
		if row[colIdx["name"]] == "CacheMemoryContext" {
			cachePath = row[colIdx["path"]]
			for _, ch := range row[colIdx["level"]] {
				if ch >= '0' && ch <= '9' {
					cacheLevel = cacheLevel*10 + int(ch-'0')
				}
			}
			break
		}
	}
	if cachePath == "" {
		t.Fatal("no CacheMemoryContext row")
	}
	cacheElem := pgPathElem(cachePath, cacheLevel)
	count := 0
	for _, row := range rows {
		if pgPathElem(row[colIdx["path"]], cacheLevel) == cacheElem {
			count++
		}
	}
	if count < 2 {
		t.Errorf("only %d rows share path[%d]=%q with CacheMemoryContext; sysviews needs >= 2",
			count, cacheLevel, cacheElem)
	}
}

// TestViewConstraintDepTracking verifies the RegisterViewConstraintDep,
// ViewsDependingOnConstraint, UnregisterViewConstraintDeps, and
// DropPrimaryKeyConstraint methods used for functional_deps regress test
// DROP CONSTRAINT RESTRICT enforcement. M0097-0036.
func TestViewConstraintDepTracking(t *testing.T) {
	c := NewInMemory()

	const tableOID uint32 = 99001
	const constraint = "articles_pkey"
	const view1 = "fdv1"
	const view2 = "fdv2"

	// Initially no deps.
	if deps := c.ViewsDependingOnConstraint(tableOID, constraint); len(deps) != 0 {
		t.Fatalf("expected 0 deps, got %v", deps)
	}

	// Register two views.
	c.RegisterViewConstraintDep(view1, tableOID, constraint)
	c.RegisterViewConstraintDep(view2, tableOID, constraint)

	deps := c.ViewsDependingOnConstraint(tableOID, constraint)
	if len(deps) != 2 {
		t.Fatalf("expected 2 deps, got %v", deps)
	}
	found1, found2 := false, false
	for _, d := range deps {
		if d == view1 {
			found1 = true
		}
		if d == view2 {
			found2 = true
		}
	}
	if !found1 || !found2 {
		t.Errorf("missing view in deps: %v", deps)
	}

	// Idempotent register.
	c.RegisterViewConstraintDep(view1, tableOID, constraint)
	if got := len(c.ViewsDependingOnConstraint(tableOID, constraint)); got != 2 {
		t.Fatalf("idempotent register: expected 2 deps, got %d", got)
	}

	// Unregister view1.
	c.UnregisterViewConstraintDeps(view1)
	deps = c.ViewsDependingOnConstraint(tableOID, constraint)
	if len(deps) != 1 || deps[0] != view2 {
		t.Errorf("after unregister view1: expected [%s], got %v", view2, deps)
	}

	// Unregister view2 — map should be empty.
	c.UnregisterViewConstraintDeps(view2)
	if deps := c.ViewsDependingOnConstraint(tableOID, constraint); len(deps) != 0 {
		t.Fatalf("after unregister all: expected 0 deps, got %v", deps)
	}
}

// TestDropPrimaryKeyConstraint verifies that DropPrimaryKeyConstraint removes
// the PK index from the catalog. M0097-0036.
func TestDropPrimaryKeyConstraint(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "articles"}, []Column{{Name: "id", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	// Create a PK index manually via the catalog's index registration path.
	if _, err := c.CreateIndex(parser.ObjectName{Name: "articles_pkey"}, tbl, []string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}

	// Verify it exists.
	idxs := c.IndexesOnTable(tbl)
	if len(idxs) != 1 || !idxs[0].Primary {
		t.Fatalf("expected 1 primary index, got %v", idxs)
	}

	// Drop it.
	if !c.DropPrimaryKeyConstraint(tbl.OID, "articles_pkey") {
		t.Fatal("DropPrimaryKeyConstraint returned false")
	}

	// Verify it's gone.
	if idxs = c.IndexesOnTable(tbl); len(idxs) != 0 {
		t.Fatalf("expected 0 indexes after drop, got %v", idxs)
	}

	// Dropping again returns false (not found).
	if c.DropPrimaryKeyConstraint(tbl.OID, "articles_pkey") {
		t.Fatal("expected false on second drop")
	}
}

// pgPathElem returns the n-th element (1-based) of a PG array literal like {1,2,3}.
func pgPathElem(arr string, n int) string {
	if len(arr) < 2 || arr[0] != '{' || arr[len(arr)-1] != '}' || n < 1 {
		return ""
	}
	inner := arr[1 : len(arr)-1]
	parts := strings.Split(inner, ",")
	if n > len(parts) {
		return ""
	}
	return parts[n-1]
}

// TestPgLanguageBuiltinRows verifies the pg_language virtual view exposes the 3
// built-in BKI languages (internal/c/sql, OIDs 12/13/14) plus plpgsql (OID 13627).
// pg_dump's dumpFunc joins pg_proc to pg_language WITHOUT a lanispl filter purely
// to fetch lanname for the function's prolang; an empty view returns "0 rows
// instead of one" and aborts the dump. All four rows MUST have lanispl=f so
// getProcLangs's `WHERE lanispl` predicate still selects nothing (no user PLs to
// dump). M0110-0001 (DU-002 slices 42, 163).
func TestPgLanguageBuiltinRows(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_language"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_language virtual table not registered")
	}
	colIdx := map[string]int{}
	for i, col := range tbl.Columns {
		colIdx[col.Name] = i
	}
	rows := tbl.VirtualRows()
	if len(rows) != 4 {
		t.Fatalf("pg_language returned %d rows; want exactly 4 (internal/c/sql/plpgsql)", len(rows))
	}
	// oid -> expected (lanname, lanpltrusted, laninline)
	want := map[string]struct {
		name      string
		pltrusted string
		inline    string
	}{
		"12":    {"internal", "f", "0"},
		"13":    {"c", "f", "0"},
		"14":    {"sql", "t", "2511"},
		"13627": {"plpgsql", "t", "0"},
	}
	for _, row := range rows {
		oid := row[colIdx["oid"]]
		w, found := want[oid]
		if !found {
			t.Errorf("unexpected pg_language row oid=%q", oid)
			continue
		}
		if got := row[colIdx["lanname"]]; got != w.name {
			t.Errorf("oid %s lanname = %q; want %q", oid, got, w.name)
		}
		// lanispl MUST be "f" for every built-in so getProcLangs returns 0 rows.
		if got := row[colIdx["lanispl"]]; got != "f" {
			t.Errorf("oid %s lanispl = %q; want f (built-ins must never be dumped)", oid, got)
		}
		if got := row[colIdx["lanpltrusted"]]; got != w.pltrusted {
			t.Errorf("oid %s lanpltrusted = %q; want %q", oid, got, w.pltrusted)
		}
		if got := row[colIdx["laninline"]]; got != w.inline {
			t.Errorf("oid %s laninline = %q; want %q", oid, got, w.inline)
		}
		if got := row[colIdx["lanowner"]]; got != "10" {
			t.Errorf("oid %s lanowner = %q; want 10 (BOOTSTRAP_SUPERUSERID)", oid, got)
		}
		delete(want, oid)
	}
	if len(want) != 0 {
		t.Errorf("pg_language missing expected rows: %v", want)
	}
}
