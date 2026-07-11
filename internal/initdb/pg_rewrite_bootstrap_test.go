package initdb

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/storage"
)

// TestPgRewriteColDefsMatchesPg18 pins the canonical PG18 8-column
// Form_pg_rewrite layout against pg_rewrite.h:32-44. Any drift here
// would silently bit-shift downstream consumers (PG casts the raw
// heap tuple as FormData_pg_rewrite*) so this guards against the
// drift that motivated Step 3dm phase A.
func TestPgRewriteColDefsMatchesPg18(t *testing.T) {
	cols := pgRewriteColDefs()
	want := []struct {
		name string
		typ  string
	}{
		{"oid", "oid"},
		{"rulename", "name"},
		{"ev_class", "oid"},
		{"ev_type", "char"},
		{"ev_enabled", "char"},
		{"is_instead", "bool"},
		{"ev_qual", "pg_node_tree"},
		{"ev_action", "pg_node_tree"},
	}
	if len(cols) != len(want) {
		t.Fatalf("pgRewriteColDefs: %d cols, want %d", len(cols), len(want))
	}
	for i, w := range want {
		if cols[i].Name != w.name {
			t.Errorf("col %d Name=%q, want %q", i, cols[i].Name, w.name)
		}
		if cols[i].Type.Name != w.typ {
			t.Errorf("col %d Type=%q, want %q", i, cols[i].Type.Name, w.typ)
		}
	}
}

// TestPgRewriteInitialEntriesContainsPgStatWalReceiverReturn pins the
// six ON-SELECT rules we seed (one per replication view). The wal_receiver
// entry is the load-bearing one for the M0106 E2E gate; the remaining five
// (batched-29) guard against "cache lookup failed for rule …" FATALs when
// a PG standby opens any of the other replication views.
func TestPgRewriteInitialEntriesContainsPgStatWalReceiverReturn(t *testing.T) {
	entries := pgRewriteInitialEntries()
	if len(entries) != 6 {
		t.Fatalf("pgRewriteInitialEntries: %d rows, want 6", len(entries))
	}
	e := entries[0]
	if e.OID != pgRewriteOIDPgStatWalReceiverReturn {
		t.Errorf("OID = %d, want %d", e.OID, pgRewriteOIDPgStatWalReceiverReturn)
	}
	if e.RuleName != "_RETURN" {
		t.Errorf("RuleName = %q, want %q", e.RuleName, "_RETURN")
	}
	if e.EvClass != pgStatWalReceiverViewOID {
		t.Errorf("EvClass = %d, want %d (must match Step 3dl view OID)", e.EvClass, pgStatWalReceiverViewOID)
	}
	if e.EvType != '1' {
		t.Errorf("EvType = %q, want '1' (CMD_SELECT)", e.EvType)
	}
	if e.EvEnabled != 'O' {
		t.Errorf("EvEnabled = %q, want 'O' (ALWAYS)", e.EvEnabled)
	}
	if !e.IsInstead {
		t.Errorf("IsInstead = false, want true (ON SELECT views are always INSTEAD)")
	}
	if e.EvQual != "<>" {
		t.Errorf("EvQual = %q, want %q (empty pg_node_tree convention)", e.EvQual, "<>")
	}
	// ev_action body is captured verbatim from an upstream PG18 dump of
	// system_views.sql's pg_stat_wal_receiver view. Length sanity-check
	// guards against accidental truncation when the .dat file is edited.
	if len(e.EvAction) < 5000 {
		t.Errorf("EvAction length = %d, want ≥ 5000 (suspicious: dump should be >5KB)", len(e.EvAction))
	}
	if e.EvAction[0] != '(' || e.EvAction[len(e.EvAction)-1] != ')' {
		t.Errorf("EvAction must be a parenthesised node tree; got first=%q last=%q",
			e.EvAction[0], e.EvAction[len(e.EvAction)-1])
	}
}

// TestReplicationViewRewriteEntries pins the five batched-29 _RETURN rules
// (pg_stat_replication through pg_stat_replication_slots). Each entry must
// have the correct view OID, non-empty ev_action, and the parenthesised
// pg_node_tree framing that PG's stringToNode expects.
func TestReplicationViewRewriteEntries(t *testing.T) {
	type want struct {
		oid     uint32
		evClass uint32
		minLen  int
	}
	wants := []want{
		{pgRewriteOIDPgStatReplicationReturn, pgStatReplicationViewOID, 5000},
		{pgRewriteOIDPgStatRecoveryPrefetchReturn, pgStatRecoveryPrefetchViewOID, 1000},
		{pgRewriteOIDPgStatSubscriptionReturn, pgStatSubscriptionViewOID, 1000},
		{pgRewriteOIDPgReplicationSlotsReturn, pgReplicationSlotsViewOID, 1000},
		{pgRewriteOIDPgStatReplicationSlotsReturn, pgStatReplicationSlotsViewOID, 1000},
	}
	entries := replicationViewRewriteEntries()
	if len(entries) != len(wants) {
		t.Fatalf("replicationViewRewriteEntries: %d rows, want %d", len(entries), len(wants))
	}
	for i, w := range wants {
		e := entries[i]
		if e.OID != w.oid {
			t.Errorf("[%d] OID = %d, want %d", i, e.OID, w.oid)
		}
		if e.EvClass != w.evClass {
			t.Errorf("[%d] EvClass = %d, want %d", i, e.EvClass, w.evClass)
		}
		if e.RuleName != "_RETURN" {
			t.Errorf("[%d] RuleName = %q, want %q", i, e.RuleName, "_RETURN")
		}
		if e.EvType != '1' {
			t.Errorf("[%d] EvType = %q, want '1'", i, e.EvType)
		}
		if e.EvEnabled != 'O' {
			t.Errorf("[%d] EvEnabled = %q, want 'O'", i, e.EvEnabled)
		}
		if !e.IsInstead {
			t.Errorf("[%d] IsInstead = false, want true", i)
		}
		if e.EvQual != "<>" {
			t.Errorf("[%d] EvQual = %q, want %q", i, e.EvQual, "<>")
		}
		if len(e.EvAction) < w.minLen {
			t.Errorf("[%d] EvAction length = %d, want ≥ %d", i, len(e.EvAction), w.minLen)
		}
		if e.EvAction[0] != '(' || e.EvAction[len(e.EvAction)-1] != ')' {
			t.Errorf("[%d] EvAction must be parenthesised; got first=%q last=%q",
				i, e.EvAction[0], e.EvAction[len(e.EvAction)-1])
		}
	}
}

// TestPgRewriteRowOrderMatchesColDefs pins that pgRewriteRow returns
// datums in the exact column order of pgRewriteColDefs (i.e. oid first,
// ev_action last). PG's heap_deformtuple iterates the TupleDesc; a
// reordering here would silently produce a corrupted Form_pg_rewrite.
func TestPgRewriteRowOrderMatchesColDefs(t *testing.T) {
	cols := pgRewriteColDefs()
	entries := pgRewriteInitialEntries()
	if len(entries) == 0 {
		t.Fatal("pgRewriteInitialEntries: empty, cannot test row order")
	}
	row := pgRewriteRow(entries[0])
	if len(row) != len(cols) {
		t.Fatalf("row len = %d, want %d (matches len(pgRewriteColDefs))", len(row), len(cols))
	}
	// oid (col 0) should be the rule OID encoded as int.
	if row[0].Int != int64(entries[0].OID) {
		t.Errorf("row[0].Int = %d, want %d (rule OID)", row[0].Int, entries[0].OID)
	}
	// rulename (col 1) should be the rule name encoded as string.
	if got := row[1].StringValue(); got != entries[0].RuleName {
		t.Errorf("row[1].String = %q, want %q", got, entries[0].RuleName)
	}
	// ev_class (col 2) should be the view OID.
	if row[2].Int != int64(entries[0].EvClass) {
		t.Errorf("row[2].Int = %d, want %d (ev_class)", row[2].Int, entries[0].EvClass)
	}
	// ev_action (col 7) is a large pg_node_tree blob. pglzVarlenaDatum
	// compresses it, so the datum kind is KindBytes and its BytesValue()
	// is the complete compressed varlena (header + tcinfo + payload).
	// Verify the compressed varlena header encodes the correct raw size.
	evActionDatum := row[7]
	if evActionDatum.Kind != executor.KindBytes {
		t.Errorf("row[7].Kind = %v, want KindBytes (pglz compressed varlena)", evActionDatum.Kind)
	} else {
		b := evActionDatum.BytesValue()
		if len(b) < 8 {
			t.Errorf("row[7] KindBytes too short: %d bytes", len(b))
		} else {
			vaHeader := binary.LittleEndian.Uint32(b[0:4])
			if vaHeader&0x03 != 0x02 {
				t.Errorf("row[7] varlena header bits 1-0 = %d, want 0x02 (compressed)", vaHeader&0x03)
			}
			tcinfo := binary.LittleEndian.Uint32(b[4:8])
			// va_tcinfo (PG18 varatt.h): rawsize in the low 30 bits,
			// compression method (PGLZ=0) in the top 2 bits.
			rawSize := int(tcinfo & ((1 << 30) - 1))
			if method := tcinfo >> 30; method != 0 {
				t.Errorf("row[7] tcinfo method = %d, want 0 (PGLZ)", method)
			}
			if rawSize != len(entries[0].EvAction) {
				t.Errorf("row[7] tcinfo rawSize = %d, want %d (ev_action length)", rawSize, len(entries[0].EvAction))
			}
		}
	}
}

// TestBootstrapPgRewriteTuplesWritesRowToBase1And5 verifies that
// bootstrapPgRewriteTuples writes a non-empty, InitPage'd heap file to
// base/{1,5}/2618. The TID map's key is the rule OID; the value's
// (Block, Offset) is later consumed by the leaf-index bootstrappers
// so we sanity-check that the offset matches the first item slot.
func TestBootstrapPgRewriteTuplesWritesRowToBase1And5(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tids, err := bootstrapPgRewriteTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgRewriteTuples: %v", err)
	}
	if len(tids) != 6 {
		t.Fatalf("tids len = %d, want 6", len(tids))
	}
	tid, ok := tids[pgRewriteOIDPgStatWalReceiverReturn]
	if !ok {
		t.Fatalf("tids missing rule OID %d", pgRewriteOIDPgStatWalReceiverReturn)
	}
	if tid.Block != 0 || tid.Offset == 0 {
		t.Errorf("first heap tuple TID = (%d, %d), want (0, >=1)", tid.Block, tid.Offset)
	}
	for _, d := range []string{"base/1/2618", "base/5/2618"} {
		path := filepath.Join(dir, d)
		buf, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// At least one full BlockSize page expected. The pg_node_tree
		// payload is ~6KB so a single page (8KB) suffices.
		if len(buf) < storage.BlockSize || len(buf)%storage.BlockSize != 0 {
			t.Errorf("%s size = %d, want non-zero multiple of %d", d, len(buf), storage.BlockSize)
		}
		// PageHeaderData.pd_special must point inside the page (not all-zero,
		// which would mean InitPage never ran).
		if bytes.Equal(buf[:64], make([]byte, 64)) {
			t.Errorf("%s first 64 bytes all-zero — InitPage likely skipped", d)
		}
	}
}

// TestPgBuildIndexTupleOidNameKeyShape pins the layout of the
// composite (oid, name) index-tuple builder used by
// bootstrapPgRewriteRelRulenameIndex. The byte layout is what PG's
// _bt_compare reads, so any drift breaks index probes silently.
func TestPgBuildIndexTupleOidNameKeyShape(t *testing.T) {
	tuple := pgBuildIndexTupleOidNameKey(7, 42, 0xDEADBEEF, "_RETURN")
	if len(tuple) != 80 {
		t.Fatalf("tuple size = %d, want 80 (MAXALIGN(8+4+64))", len(tuple))
	}
	le := binary.LittleEndian
	if got := le.Uint16(tuple[0:2]); got != 0 {
		t.Errorf("bi_hi = %d, want 0 (block 7 fits in 16 bits)", got)
	}
	if got := le.Uint16(tuple[2:4]); got != 7 {
		t.Errorf("bi_lo = %d, want 7", got)
	}
	if got := le.Uint16(tuple[4:6]); got != 42 {
		t.Errorf("ip_posid = %d, want 42", got)
	}
	if got := le.Uint16(tuple[6:8]) & indexSizeMask; got != 80 {
		t.Errorf("t_info size bits = %d, want 80", got)
	}
	if got := le.Uint32(tuple[8:12]); got != 0xDEADBEEF {
		t.Errorf("oid key = %#x, want 0xDEADBEEF", got)
	}
	if got := string(bytes.TrimRight(tuple[12:76], "\x00")); got != "_RETURN" {
		t.Errorf("name payload = %q, want %q", got, "_RETURN")
	}
	// Trailing MAXALIGN padding must remain zero — PG's IndexTupleSize
	// is the t_info-encoded value (80), and any non-zero pad would shift
	// `_bt_compare`'s reads.
	for i := 76; i < 80; i++ {
		if tuple[i] != 0 {
			t.Errorf("MAXALIGN pad byte %d = %#x, want 0", i, tuple[i])
		}
	}
}

// TestBootstrapPgRewriteLeafIndicesWriteBothFiles verifies that the
// two leaf-index bootstrappers write a 2-block btree (metapage + leaf
// root) to base/{1,5}/{2692,2693}. Without these files PG's
// SearchSysCache2(RULERELNAME, …) FATALs the moment relcache opens
// the view.
func TestBootstrapPgRewriteLeafIndicesWriteBothFiles(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tids, err := bootstrapPgRewriteTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgRewriteTuples: %v", err)
	}
	if err := bootstrapPgRewriteOidIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgRewriteOidIndex: %v", err)
	}
	if err := bootstrapPgRewriteRelRulenameIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgRewriteRelRulenameIndex: %v", err)
	}
	for _, p := range []string{
		"base/1/2692", "base/5/2692",
		"base/1/2693", "base/5/2693",
	} {
		path := filepath.Join(dir, p)
		buf, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(buf) != 2*storage.BlockSize {
			t.Errorf("%s size = %d, want %d (metapage + leaf root)", p, len(buf), 2*storage.BlockSize)
		}
		// The metapage's magic constant ought to land at bytes [24..28]
		// inside the first block — but the precise offset depends on
		// PageHeaderData layout. A zero-prefix check is a cheap guard
		// against InitPage being skipped.
		if bytes.Equal(buf[:64], make([]byte, 64)) {
			t.Errorf("%s metapage all-zero — InitPage likely skipped", p)
		}
	}
}
