package postmaster

import (
	"net"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
)

// M0132-S6 — isolation level over the extended protocol.
//
// `BEGIN ISOLATION LEVEL <level>` reaches the shared transaction-verb state
// machine (applyTransactionVerb, txn_verb.go) exactly as it does on the simple
// path — the M0104-0008 code that honours txNode.IsolationLevel, rolls back the
// placeholder auto-commit transaction and re-begins at the requested level was
// extracted verbatim with the rest of the arm. What was missing until this slice
// was the connection's ProcArray slot: the simple path sets ectx.ProcNum from
// connTx.ProcNum (dispatch.go) but the extended path never did, so the re-begin
// landed on slot 0 instead of the connection's own slot — two concurrent
// SERIALIZABLE blocks opened over this protocol would collide there.
//
// The gate the task requires is behavioural, not structural: a canonical SSI
// write-skew must abort when its block is opened over the extended protocol.
// That proves the block really runs at SERIALIZABLE — under READ COMMITTED the
// same interleaving commits both writers (the control below).

// extendedWriteSkewSide opens a block at the given isolation level over the
// EXTENDED protocol on conn and runs updateSQL inside it, leaving the block
// OPEN. The caller interleaves the two sides' COMMITs explicitly so the
// overlapping permutation (rwx1 rwx2 c1 c2) is what the pre-commit check sees —
// a helper that commits would serialise the two blocks and turn the overlap
// into the no-overlap case.
func extendedWriteSkewSide(t *testing.T, conn net.Conn, r *libpq.FrameReader, prefix, level, updateSQL string) {
	t.Helper()
	if f := extendedStmt(t, conn, r, prefix+"_begin", "BEGIN ISOLATION LEVEL "+level); hasError(f) {
		t.Fatalf("%s BEGIN ISOLATION LEVEL %s errored: %+v", prefix, level, f)
	}
	if f := extendedStmt(t, conn, r, prefix+"_upd", updateSQL); hasError(f) {
		t.Fatalf("%s %s errored: %+v", prefix, updateSQL, f)
	}
}

// TestM0132S6_ExtendedSerializableBlockAbortsWriteSkew is M0132-S6's gate: the
// canonical simple-write-skew interleaving, with both blocks opened over the
// EXTENDED protocol. The second committer must abort with 40001 — under a
// correctly-honoured SERIALIZABLE the two UPDATEs form a read/write-dangerous
// structure the pre-commit check refuses. Before this slice the re-begin
// collided on proc slot 0, so the blocks did not actually run SERIALIZABLE.
func TestM0132S6_ExtendedSerializableBlockAbortsWriteSkew(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	c1 := dialAndComplete(t, addr)
	defer c1.Close()
	c2 := dialAndComplete(t, addr)
	defer c2.Close()
	r1 := extendedReader(t, c1)
	r2 := extendedReader(t, c2)

	// Seed the canonical dataset (i=5 apple, 7 pear, 11 banana) over the simple
	// path, mirroring internal/testport's ssi_ws setup.
	for _, ins := range []string{
		"INSERT INTO items VALUES (5, 'apple')",
		"INSERT INTO items VALUES (7, 'pear')",
		"INSERT INTO items VALUES (11, 'banana')",
	} {
		if f := simpleStmt(t, c1, r1, ins); hasError(f) {
			t.Fatalf("seed %q errored: %+v", ins, f)
		}
	}

	// rwx1 rwx2 c1 c2 — the overlapping permutation (second committer aborts).
	extendedWriteSkewSide(t, c1, r1, "s1", "SERIALIZABLE", "UPDATE items SET label = 'apple' WHERE label = 'pear'")
	extendedWriteSkewSide(t, c2, r2, "s2", "SERIALIZABLE", "UPDATE items SET label = 'pear' WHERE label = 'apple'")

	if f := extendedStmt(t, c1, r1, "c1", "COMMIT"); hasError(f) {
		t.Fatalf("first committer aborted unexpectedly: %+v", f)
	}
	if f := extendedStmt(t, c2, r2, "c2", "COMMIT"); !errorContains(f, "40001") {
		t.Errorf("second committer over an extended SERIALIZABLE block: want 40001 serialization failure, got %+v", f)
	}
}

// TestM0132S6_ExtendedReadCommittedBlockAllowsWriteSkew is the control: the
// same interleaving with both blocks opened at READ COMMITTED (over the
// extended protocol) must NOT abort — write-skew is permitted below
// SERIALIZABLE, and an accidental eager 40001 would be a false-positive
// regression.
func TestM0132S6_ExtendedReadCommittedBlockAllowsWriteSkew(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	c1 := dialAndComplete(t, addr)
	defer c1.Close()
	c2 := dialAndComplete(t, addr)
	defer c2.Close()
	r1 := extendedReader(t, c1)
	r2 := extendedReader(t, c2)

	for _, ins := range []string{
		"INSERT INTO items VALUES (5, 'apple')",
		"INSERT INTO items VALUES (7, 'pear')",
		"INSERT INTO items VALUES (11, 'banana')",
	} {
		if f := simpleStmt(t, c1, r1, ins); hasError(f) {
			t.Fatalf("seed %q errored: %+v", ins, f)
		}
	}

	extendedWriteSkewSide(t, c1, r1, "s1", "READ COMMITTED", "UPDATE items SET label = 'apple' WHERE label = 'pear'")
	extendedWriteSkewSide(t, c2, r2, "s2", "READ COMMITTED", "UPDATE items SET label = 'pear' WHERE label = 'apple'")

	if f := extendedStmt(t, c1, r1, "c1", "COMMIT"); hasError(f) {
		t.Fatalf("first committer aborted unexpectedly: %+v", f)
	}
	if f := extendedStmt(t, c2, r2, "c2", "COMMIT"); hasError(f) {
		t.Errorf("second committer over an extended READ COMMITTED block: want no error, got %+v", f)
	}
}
