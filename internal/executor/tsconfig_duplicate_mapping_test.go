package executor

import "testing"

// TestAlterTSConfigAddMappingDuplicateRaises23505 pins the M0119-0004 slice
// 446 deferral-ledger fix: real PG 18.3's MakeConfigurationMapping
// (tsearchcmds.c) restarts mapseqno at 1 for every plain ADD MAPPING call, so
// re-adding a mapping for an already-mapped token type collides with the
// existing seqno-1 row on the (mapcfg, maptokentype, mapseqno) unique index
// and raises a 23505 unique_violation — verified against a scratch upstream
// PostgreSQL 18.3 instance, NOT the 42710 this slice's ledger row originally
// guessed. goopg previously appended a second, unenforced entry silently.
func TestAlterTSConfigAddMappingDuplicateRaises23505(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_dup_cfg (PARSER = default)`); err != nil {
		t.Fatalf("create text search configuration: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_dup_cfg ADD MAPPING FOR word WITH simple`); err != nil {
		t.Fatalf("first ADD MAPPING: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_dup_cfg ADD MAPPING FOR word WITH simple`)
	if err == nil {
		t.Fatal("second ADD MAPPING for the same token type: want 23505, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23505" {
		t.Fatalf("want *ExecError 23505, got %T %v", err, err)
	}
	wantMsg := `duplicate key value violates unique constraint "pg_ts_config_map_index"`
	if ee.Message != wantMsg {
		t.Errorf("Message=%q want %q", ee.Message, wantMsg)
	}
	wantDetail := "Key (mapcfg, maptokentype, mapseqno)=(16384, 2, 1) already exists."
	if ee.Detail != wantDetail {
		t.Errorf("Detail=%q want prefix-shaped like %q (OID may legitimately differ)", ee.Detail, wantDetail)
	}

	// A distinct token type is unaffected — it must still succeed.
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_dup_cfg ADD MAPPING FOR asciiword WITH simple`); err != nil {
		t.Fatalf("ADD MAPPING for a different token type: %v", err)
	}
}
