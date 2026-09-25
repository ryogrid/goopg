package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
)

// TestDiscardAll pins discard.c DiscardAll's executor-side half: it refuses
// to run inside a transaction block (PreventInTransactionBlock, 25001,
// touching no state), and otherwise resets session authorization and the role
// and runs RESET ALL, besides the sequence/temp work DISCARD SEQUENCES / TEMP
// already did. The connection-held half (prepared statements, cursors,
// LISTEN) is the postmaster's; the whole statement was live-verified against
// PG 18.3 (M-NIGHTLY command-tag sweep).
func TestDiscardAll(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = transam.NewManager()
	sess := NewBasicSession()
	ctx.Session = sess
	resets, auth, role := 0, 0, 0
	ctx.ResetAllSettings = func() { resets++ }
	ctx.SetSessionAuthorization = func(string, bool) { auth++ }
	ctx.SetRole = func(string, bool) { role++ }
	ctx.CurrSeqVals = map[string]int64{"s": 1}

	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	err := runTransactionStmt(t, ctx, "DISCARD ALL")
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "25001" || ee.Message != "DISCARD ALL cannot run inside a transaction block" {
		t.Fatalf("DISCARD ALL in a block: err %v, want 25001", err)
	}
	if resets+auth+role != 0 || len(ctx.CurrSeqVals) != 1 {
		t.Fatalf("the refused DISCARD ALL touched state: resets=%d auth=%d role=%d seqs=%d", resets, auth, role, len(ctx.CurrSeqVals))
	}
	if err := runTransactionStmt(t, ctx, "ROLLBACK"); err != nil {
		t.Fatalf("ROLLBACK: %v", err)
	}

	if err := runTransactionStmt(t, ctx, "DISCARD ALL"); err != nil {
		t.Fatalf("DISCARD ALL: %v", err)
	}
	if resets != 1 || auth != 1 || role != 1 || len(ctx.CurrSeqVals) != 0 {
		t.Fatalf("DISCARD ALL: resets=%d auth=%d role=%d seqs=%d, want 1/1/1/0", resets, auth, role, len(ctx.CurrSeqVals))
	}
}
