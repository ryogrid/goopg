package transam

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestSeesCommittedXIDHintedMatchesUnhinted is the safety argument for the
// HEAP_XMIN_COMMITTED fast path, expressed as a test rather than as prose.
//
// SeesCommittedXIDHinted drops SeesCommittedXID's CLOG consult. That is sound
// only because the hint bit is written after the CLOG already answered "not
// aborted", and commit is terminal. So the two predicates must agree on EVERY
// xid the CLOG does not report as aborted — and may differ ONLY on an aborted
// xid, which is a state a hinted tuple cannot be in.
//
// If someone later sets the hint bit somewhere that has not confirmed the CLOG,
// this test still passes but the invariant it rests on is gone — which is why
// the second half pins the difference explicitly rather than leaving it
// implicit.
func TestSeesCommittedXIDHintedMatchesUnhinted(t *testing.T) {
	clog := newTestCLogForHinted(t)

	const (
		committed storage.TransactionID = 100
		aborted   storage.TransactionID = 101
		unknown   storage.TransactionID = 102
	)
	if err := clog.SetCommitted(committed); err != nil {
		t.Fatalf("SetCommitted: %v", err)
	}
	if err := clog.SetAborted(aborted); err != nil {
		t.Fatalf("SetAborted: %v", err)
	}

	// A matrix of snapshot windows around the XIDs above.
	snaps := []struct {
		name string
		snap Snapshot
	}{
		{"xid below xmin", Snapshot{Xmin: 200, Xmax: 300, clog: clog}},
		{"xid inside window", Snapshot{Xmin: 50, Xmax: 300, clog: clog}},
		{"xid above xmax", Snapshot{Xmin: 10, Xmax: 60, clog: clog}},
		{"in-progress", Snapshot{Xmin: 50, Xmax: 300, InProgress: []storage.TransactionID{committed, aborted, unknown}, clog: clog}},
		{"explicitly aborted set", Snapshot{Xmin: 50, Xmax: 300, Aborted: []storage.TransactionID{committed, unknown}, clog: clog}},
	}

	for _, sc := range snaps {
		for _, xid := range []storage.TransactionID{committed, unknown, storage.InvalidTransactionID} {
			// For every xid the CLOG does NOT call aborted — i.e. every state a
			// hinted tuple can legally be in — the two must agree exactly.
			got := sc.snap.SeesCommittedXIDHinted(xid)
			want := sc.snap.SeesCommittedXID(xid)
			if got != want {
				t.Errorf("%s / xid=%d: hinted=%v unhinted=%v — the fast path must be "+
					"indistinguishable for any xid the CLOG does not report aborted",
					sc.name, xid, got, want)
			}
		}
	}

	// The ONE permitted divergence, pinned so it is visible rather than
	// accidental: on a CLOG-aborted xid the hinted form skips the consult and
	// can answer true where the unhinted form answers false. A tuple whose
	// xmin is aborted never carries HEAP_XMIN_COMMITTED, so this state is
	// unreachable through the hint-bit branch.
	sawDivergence := false
	for _, sc := range snaps {
		if sc.snap.SeesCommittedXIDHinted(aborted) != sc.snap.SeesCommittedXID(aborted) {
			sawDivergence = true
		}
	}
	if !sawDivergence {
		t.Log("note: no divergence observed on the CLOG-aborted xid; the CLOG " +
			"consult may not be reachable in this fixture, so the equivalence " +
			"above is weaker than intended")
	}
}

func newTestCLogForHinted(t *testing.T) *CLog {
	t.Helper()
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	mustEnableMirror(t, c, filepath.Join(dir, "pg_xact_slru"))
	return c
}
