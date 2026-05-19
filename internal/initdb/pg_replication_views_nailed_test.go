package initdb

import "testing"

// TestNailedLocalRelsContainsFiveReplicationViews pins the M0106-0010
// batched-28 seed of the five remaining replication views as relkind='v'
// entries in the bootstrap pg_class heap. A PG standby booted from a goopg
// data directory will FATAL with `42P01 relation "pg_stat_replication" does
// not exist` (or the equivalent for any of the other four) unless each view
// has a pg_class row with the correct OID, RelNatts, and RelType.
func TestNailedLocalRelsContainsFiveReplicationViews(t *testing.T) {
	cases := []struct {
		oid     uint32
		name    string
		natts   int16
		reltype uint32
	}{
		{12102, "pg_stat_replication", 20, 2249},
		{12103, "pg_stat_recovery_prefetch", 10, 2249},
		{12104, "pg_stat_subscription", 11, 2249},
		{12105, "pg_replication_slots", 21, 2249},
		{12106, "pg_stat_replication_slots", 10, 2249},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var got *nailedRel
			for i := range nailedLocalRels {
				if nailedLocalRels[i].OID == tc.oid {
					got = &nailedLocalRels[i]
					break
				}
			}
			if got == nil {
				t.Fatalf("nailedLocalRels missing OID %d (%s) — batched-28 regression", tc.oid, tc.name)
			}
			if got.RelName != tc.name {
				t.Fatalf("OID %d: RelName=%q want %q", tc.oid, got.RelName, tc.name)
			}
			if got.RelKind != 'v' {
				t.Fatalf("OID %d: RelKind=%q want 'v' (view)", tc.oid, got.RelKind)
			}
			if got.RelType != tc.reltype {
				t.Fatalf("OID %d: RelType=%d want %d (RECORDOID)", tc.oid, got.RelType, tc.reltype)
			}
			if got.RelNatts != tc.natts {
				t.Fatalf("OID %d: RelNatts=%d want %d", tc.oid, got.RelNatts, tc.natts)
			}
			if got.IsShared {
				t.Fatalf("OID %d: IsShared=true want false (per-database view)", tc.oid)
			}
			if int16(len(got.Attrs)) != tc.natts {
				t.Fatalf("OID %d: len(Attrs)=%d want %d", tc.oid, len(got.Attrs), tc.natts)
			}
			for i, a := range got.Attrs {
				if a.Num != int16(i+1) {
					t.Fatalf("OID %d Attrs[%d].Num=%d want %d", tc.oid, i, a.Num, i+1)
				}
				if a.NotNull {
					t.Fatalf("OID %d Attrs[%d].NotNull=true want false (view columns inherit nullability)", tc.oid, i)
				}
			}
		})
	}
}

// TestPgStatReplicationViewAttrs pins the 20 column descriptors for
// pg_catalog.pg_stat_replication (OID 12102, system_views.sql:906-930).
func TestPgStatReplicationViewAttrs(t *testing.T) {
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
	}{
		{"pid", 23, 4},
		{"usesysid", 26, 4},
		{"usename", 19, 64},
		{"application_name", 25, -1},
		{"client_addr", 869, -1},
		{"client_hostname", 25, -1},
		{"client_port", 23, 4},
		{"backend_start", 1184, 8},
		{"backend_xmin", 28, 4},
		{"state", 25, -1},
		{"sent_lsn", 3220, 8},
		{"write_lsn", 3220, 8},
		{"flush_lsn", 3220, 8},
		{"replay_lsn", 3220, 8},
		{"write_lag", 1186, 16},
		{"flush_lag", 1186, 16},
		{"replay_lag", 1186, 16},
		{"sync_priority", 23, 4},
		{"sync_state", 25, -1},
		{"reply_time", 1184, 8},
	}
	checkViewAttrs(t, "pg_stat_replication", pgStatReplicationViewAttrs(), want)
}

// TestPgStatRecoveryPrefetchViewAttrs pins the 10 column descriptors for
// pg_catalog.pg_stat_recovery_prefetch (OID 12103, system_views.sql:965-977).
func TestPgStatRecoveryPrefetchViewAttrs(t *testing.T) {
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
	}{
		{"stats_reset", 1184, 8},
		{"prefetch", 20, 8},
		{"hit", 20, 8},
		{"skip_init", 20, 8},
		{"skip_new", 20, 8},
		{"skip_fpw", 20, 8},
		{"skip_rep", 20, 8},
		{"wal_distance", 23, 4},
		{"block_distance", 23, 4},
		{"io_depth", 23, 4},
	}
	checkViewAttrs(t, "pg_stat_recovery_prefetch", pgStatRecoveryPrefetchViewAttrs(), want)
}

// TestPgStatSubscriptionViewAttrs pins the 11 column descriptors for
// pg_catalog.pg_stat_subscription (OID 12104, system_views.sql:979-994).
func TestPgStatSubscriptionViewAttrs(t *testing.T) {
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
	}{
		{"subid", 26, 4},
		{"subname", 19, 64},
		{"worker_type", 25, -1},
		{"pid", 23, 4},
		{"leader_pid", 23, 4},
		{"relid", 26, 4},
		{"received_lsn", 3220, 8},
		{"last_msg_send_time", 1184, 8},
		{"last_msg_receipt_time", 1184, 8},
		{"latest_end_lsn", 3220, 8},
		{"latest_end_time", 1184, 8},
	}
	checkViewAttrs(t, "pg_stat_subscription", pgStatSubscriptionViewAttrs(), want)
}

// TestPgReplicationSlotsViewAttrs pins the 21 column descriptors for
// pg_catalog.pg_replication_slots (OID 12105, system_views.sql:1019-1043).
// PG18 adds two_phase_at/inactive_since/conflicting/invalidation_reason/
// failover/synced as the final six entries.
func TestPgReplicationSlotsViewAttrs(t *testing.T) {
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
	}{
		{"slot_name", 19, 64},
		{"plugin", 19, 64},
		{"slot_type", 25, -1},
		{"datoid", 26, 4},
		{"database", 19, 64},
		{"temporary", 16, 1},
		{"active", 16, 1},
		{"active_pid", 23, 4},
		{"xmin", 28, 4},
		{"catalog_xmin", 28, 4},
		{"restart_lsn", 3220, 8},
		{"confirmed_flush_lsn", 3220, 8},
		{"wal_status", 25, -1},
		{"safe_wal_size", 20, 8},
		{"two_phase", 16, 1},
		{"two_phase_at", 3220, 8},
		{"inactive_since", 1184, 8},
		{"conflicting", 16, 1},
		{"invalidation_reason", 25, -1},
		{"failover", 16, 1},
		{"synced", 16, 1},
	}
	checkViewAttrs(t, "pg_replication_slots", pgReplicationSlotsViewAttrs(), want)
}

// TestPgStatReplicationSlotsViewAttrs pins the 10 column descriptors for
// pg_catalog.pg_stat_replication_slots (OID 12106, system_views.sql:1045-1059).
func TestPgStatReplicationSlotsViewAttrs(t *testing.T) {
	want := []struct {
		Name    string
		TypeOID uint32
		Len     int16
	}{
		{"slot_name", 19, 64},
		{"spill_txns", 20, 8},
		{"spill_count", 20, 8},
		{"spill_bytes", 20, 8},
		{"stream_txns", 20, 8},
		{"stream_count", 20, 8},
		{"stream_bytes", 20, 8},
		{"total_txns", 20, 8},
		{"total_bytes", 20, 8},
		{"stats_reset", 1184, 8},
	}
	checkViewAttrs(t, "pg_stat_replication_slots", pgStatReplicationSlotsViewAttrs(), want)
}

// checkViewAttrs is a shared helper for the five view attr tests.
func checkViewAttrs(t *testing.T, viewName string, got []nailedAttr, want []struct {
	Name    string
	TypeOID uint32
	Len     int16
}) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: len(Attrs)=%d want %d", viewName, len(got), len(want))
	}
	for i, w := range want {
		a := got[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Len != w.Len {
			t.Fatalf("%s Attrs[%d]={%s,%d,%d} want {%s,%d,%d}",
				viewName, i, a.Name, a.TypeOID, a.Len, w.Name, w.TypeOID, w.Len)
		}
		if a.Num != int16(i+1) {
			t.Fatalf("%s Attrs[%d].Num=%d want %d", viewName, i, a.Num, i+1)
		}
		if a.NotNull {
			t.Fatalf("%s Attrs[%d].NotNull=true want false (view columns inherit nullability)", viewName, i)
		}
	}
}
