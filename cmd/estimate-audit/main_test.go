package main

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// TestSelectQueriesParsesValidSpecs pins the accepted forms.
func TestSelectQueriesParsesValidSpecs(t *testing.T) {
	cases := []struct {
		spec string
		want []int
	}{
		{"1", []int{1}},
		{"3,1", []int{1, 3}},
		{"5-7", []int{5, 6, 7}},
		{" 2 , 4-5 ", []int{2, 4, 5}},
	}
	for _, tc := range cases {
		if got := selectQueries(tc.spec); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("selectQueries(%q) = %v, want %v", tc.spec, got, tc.want)
		}
	}
}

// TestSelectQueriesRejectsMalformedSpecs covers review/260831-2 CM-5: the Atoi
// errors used to be dropped, so `--queries 5-` produced hi=0, the 5..0 loop
// selected nothing, and the tool audited ZERO queries while reporting success;
// `--queries -5` asked for a nonexistent "Q-5". Both must now exit non-zero
// with a message. selectQueries calls fatal (os.Exit), so each case runs in a
// re-exec of this test binary.
func TestSelectQueriesRejectsMalformedSpecs(t *testing.T) {
	for _, spec := range []string{"5-", "-5", "3-x", "abc", "0", "9-4"} {
		if os.Getenv("ESTIMATE_AUDIT_BAD_SPEC") == spec {
			selectQueries(spec)
			return
		}
	}
	for _, spec := range []string{"5-", "-5", "3-x", "abc", "0", "9-4"} {
		cmd := exec.Command(os.Args[0], "-test.run=TestSelectQueriesRejectsMalformedSpecs")
		cmd.Env = append(os.Environ(), "ESTIMATE_AUDIT_BAD_SPEC="+spec)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("selectQueries(%q) accepted the spec; want a fatal error", spec)
			continue
		}
		if !strings.Contains(string(out), "--queries") {
			t.Errorf("selectQueries(%q): message %q lacks the option name", spec, out)
		}
	}
}

// TestHashStatsEpochRowsMatchesCaptureStampFormula pins the M0137-0006
// fingerprint format to the SAME formula scripts/lib/capture-stamp.sh's
// _capture_stamp_stats_epoch uses: sha256 over "relname|n_live_tup\n" rows
// (ordered by relname, one trailing newline after the last row), first 16
// hex chars. The want value was hand-verified against the bash formula
// itself (`sha256sum <<<"${res}" | cut -c1-16` on "nation|25\nregion|5\n").
// A mismatch here means an estimate-audit epoch and a
// capture-tpch.sh/capture-tpcds.sh epoch would silently stop being
// comparable by scripts/check-stats-epoch.sh even when the underlying
// statistics are identical — exactly the false-drift-signal class M0137-0006
// exists to prevent.
func TestHashStatsEpochRowsMatchesCaptureStampFormula(t *testing.T) {
	got := hashStatsEpochRows([]statsEpochRow{
		{relname: "nation", nLiveTup: 25},
		{relname: "region", nLiveTup: 5},
	})
	const want = "cd91584b25f11563"
	if got != want {
		t.Fatalf("hashStatsEpochRows = %q, want %q (cross-tool fingerprint format diverged)", got, want)
	}
}

// TestHashStatsEpochRowsOrderSensitive: the SQL side orders by relname so two
// captures of the SAME statistics always hash the rows in the same order;
// this pins that the hash function itself does not silently re-sort or
// otherwise mask a genuine ordering difference, which would hide a real
// stats change instead of surfacing it as a mismatch.
func TestHashStatsEpochRowsOrderSensitive(t *testing.T) {
	a := hashStatsEpochRows([]statsEpochRow{{relname: "nation", nLiveTup: 25}, {relname: "region", nLiveTup: 5}})
	b := hashStatsEpochRows([]statsEpochRow{{relname: "region", nLiveTup: 5}, {relname: "nation", nLiveTup: 25}})
	if a == b {
		t.Fatalf("hashStatsEpochRows ignored row order: both orderings hashed to %q", a)
	}
}

// TestStatsEpochLineOfflineReplayIsUnknown: --from-plans has no live
// connection, so the epoch must degrade to an explicit UNKNOWN(reason)
// rather than silently omit the line or (worse) hang trying to dial a port
// that was never given.
func TestStatsEpochLineOfflineReplayIsUnknown(t *testing.T) {
	f := &flags{fromPlans: "some.plans.txt"}
	got := statsEpochLine(f)
	if !strings.HasPrefix(got, "# stats-epoch: UNKNOWN(offline replay via --from-plans") {
		t.Fatalf("statsEpochLine(--from-plans) = %q, want an UNKNOWN(offline replay...) line", got)
	}
}

// TestStatsEpochLineUnreachablePortIsUnknown: a live run whose stats-epoch
// probe cannot reach the server must not crash the whole audit (a run that
// may have just cost a full TPC-H power run) — it degrades to UNKNOWN(reason)
// the same way renderEnum degrades on a missing enum-trace log.
func TestStatsEpochLineUnreachablePortIsUnknown(t *testing.T) {
	f := &flags{host: "127.0.0.1", port: 1, db: "nope", user: "nope", pass: "nope"}
	got := statsEpochLine(f)
	if !strings.HasPrefix(got, "# stats-epoch: UNKNOWN(") {
		t.Fatalf("statsEpochLine(unreachable) = %q, want an UNKNOWN(...) line, not a crash", got)
	}
}
