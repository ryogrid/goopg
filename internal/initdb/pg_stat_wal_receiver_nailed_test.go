package initdb

import "testing"

// TestNailedLocalRelsContainsPgStatWalReceiver pins the M0106-0010 Step 3dl
// seed of the `pg_catalog.pg_stat_wal_receiver` view as the first
// relkind='v' entry in the bootstrap pg_class heap. Failing this test
// means a PG standby booted from goopg's data directory will FATAL or
// reply `42P01` for the E2E test's
// `SELECT status FROM pg_catalog.pg_stat_wal_receiver` probe.
func TestNailedLocalRelsContainsPgStatWalReceiver(t *testing.T) {
	const viewOID = 12100

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == viewOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_stat_wal_receiver) — Step 3dl regression", viewOID)
	}
	if got.RelName != "pg_stat_wal_receiver" {
		t.Fatalf("OID %d: RelName=%q want %q", viewOID, got.RelName, "pg_stat_wal_receiver")
	}
	if got.RelKind != 'v' {
		t.Fatalf("OID %d: RelKind=%q want 'v' (view)", viewOID, got.RelKind)
	}
	// RelType must be 2249 (RECORDOID) so any reltype lookup gets a
	// valid composite-type pointer (matches pg_proc.dat:5670 prorettype
	// for the underlying pg_stat_get_wal_receiver function).
	if got.RelType != 2249 {
		t.Fatalf("OID %d: RelType=%d want 2249 (RECORDOID)", viewOID, got.RelType)
	}
	if got.RelNatts != 15 {
		t.Fatalf("OID %d: RelNatts=%d want 15 (system_views.sql:945-963 column count)", viewOID, got.RelNatts)
	}
	if got.IsShared {
		t.Fatalf("OID %d: IsShared=true want false (per-database view)", viewOID)
	}
	if len(got.Attrs) != 15 {
		t.Fatalf("OID %d: len(Attrs)=%d want 15", viewOID, len(got.Attrs))
	}

	// Column-by-column pin against system_views.sql:945-963 + pg_proc.dat:5671.
	// (name, TypeOID, Len) — view columns inherit nullability from the
	// expression so attnotnull is implicitly false (zero value).
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
	}{
		{"pid", 23, 4},
		{"status", 25, -1},
		{"receive_start_lsn", 3220, 8},
		{"receive_start_tli", 23, 4},
		{"written_lsn", 3220, 8},
		{"flushed_lsn", 3220, 8},
		{"received_tli", 23, 4},
		{"last_msg_send_time", 1184, 8},
		{"last_msg_receipt_time", 1184, 8},
		{"latest_end_lsn", 3220, 8},
		{"latest_end_time", 1184, 8},
		{"slot_name", 25, -1},
		{"sender_host", 25, -1},
		{"sender_port", 23, 4},
		{"conninfo", 25, -1},
	}
	for i, w := range want {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Len != w.Len {
			t.Fatalf("Attrs[%d]={%s,%d,%d} want {%s,%d,%d}",
				i, a.Name, a.TypeOID, a.Len, w.Name, w.TypeOID, w.Len)
		}
		if a.Num != int16(i+1) {
			t.Fatalf("Attrs[%d].Num=%d want %d", i, a.Num, i+1)
		}
		if a.NotNull {
			t.Fatalf("Attrs[%d].NotNull=true want false (view columns inherit nullability)", i)
		}
	}
}

// TestPgClassRowForViewSetsZeroRelfilenode pins the M0106-0010 Step 3dl
// pgClassRow change: views (relkind='v') write relfilenode=0 and relam=0,
// while relhasrules=true so PG's relcache fetches the ON-SELECT rewrite
// rule. Without these overrides, PG would attempt to open the view's
// non-existent `base/<db>/12100` heap file at relcache-build time and
// FATAL.
func TestPgClassRowForViewSetsZeroRelfilenode(t *testing.T) {
	rel := nailedRel{
		OID:      12100,
		RelName:  "pg_stat_wal_receiver",
		RelType:  2249,
		RelKind:  'v',
		RelNatts: 15,
	}
	row := pgClassRow(rel)
	// pg_class column layout (0-indexed): 6=relam, 7=relfilenode,
	// 20=relhasrules (oid,relname,relnamespace,reltype,reloftype,
	// relowner,relam,relfilenode,reltablespace,relpages,reltuples,
	// relallvisible,relallfrozen,reltoastrelid,relhasindex,relisshared,
	// relpersistence,relkind,relnatts,relchecks,relhasrules,...).
	if row[6].Int != 0 {
		t.Fatalf("view pg_class.relam=%d want 0", row[6].Int)
	}
	if row[7].Int != 0 {
		t.Fatalf("view pg_class.relfilenode=%d want 0", row[7].Int)
	}
	// NAILED replication system views (12100-12106) keep relhasrules=false: their
	// canonical ev_action IS present, but PG serves these views from its own
	// built-in relcache entries, so enabling standby-side rule expansion for them
	// is a separate track. M0123-S3 sub-slice 2c landed canonical ev_action +
	// relhasrules=true for USER views only (buildUserPGClassRow reads
	// tbl.RuleIsCanonical); this bootstrap pgClassRow path is unaffected.
	if row[20].BoolValue() {
		t.Fatalf("nailed view pg_class.relhasrules=true want false")
	}
}
