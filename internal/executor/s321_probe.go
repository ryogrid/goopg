package executor

import (
	"fmt"
	"os"
	"sync/atomic"
)

// M0131-S32.1 diagnostic probe (temporary, GOOPG_S321_PROBE=1).
//
// The concurrent hot-row defect (analysis/concurrent-hotrow.sh: 8 sessions each
// applying 200 blind `v = v + 1` increments to ONE row land only ~470 of 1600,
// with no error and `UPDATE 1` reported every time) is a SILENT skip: some
// return path in the UPDATE write phase abandons the row without writing and
// without raising. There are eight such paths across tryApplyHOTUpdate and the
// EvalPlanQual retry loop, and reading them cannot say which one fires — hence
// this counter set. Each site bumps one counter; s321Dump prints the tally to
// the server log when the probe is on, so a run's losses can be attributed.
//
// Remove once S32.1 is fixed and covered by a regression test.
var s321Enabled = os.Getenv("GOOPG_S321_PROBE") == "1"

// s321NoWait disables the S32.1 pending-writer wait so a run can be A/B'd
// against the pre-fix behaviour without rebuilding. Diagnostic only.
var s321NoWait = os.Getenv("GOOPG_S321_NOWAIT") == "1"

var s321Counters [s321NumSites]atomic.Int64

type s321Site int

const (
	s321HOTSkipItemID       s321Site = iota // tryApplyHOTUpdate: old slot no longer LP_NORMAL
	s321HOTSkipConcurrent                   // tryApplyHOTUpdate: isConcurrentlyUpdated -> fall back
	s321HOTNoSpace                          // tryApplyHOTUpdate: page full -> fall back
	s321HOTApplied                          // tryApplyHOTUpdate: wrote a new HOT version
	s321EPQChainNotFound                    // EPQ: successor not reachable -> row SKIPPED
	s321EPQOldGetFail                       // EPQ: PageGetHeapTuple failed -> row SKIPPED
	s321EPQChainFollowed                    // EPQ: successor found, SET re-evaluated
	s321EPQWrote                            // EPQ: non-HOT delete+insert completed
	s321ScanNoRow                           // scan phase produced zero pending rows
	s321ScanDedup                           // pending dedupe suppressed a duplicate index hit
	s321EPQSelfModified                     // EPQ: target already killed by THIS txn -> skip
	s321EPQChainPendingWait                 // EPQ: chain tip owned by an in-flight writer -> wait + retry
	s321NumSites
)

var s321Names = [s321NumSites]string{
	"hot_skip_itemid", "hot_skip_concurrent", "hot_nospace", "hot_applied",
	"epq_chain_notfound", "epq_oldget_fail", "epq_chain_followed", "epq_wrote",
	"scan_no_row", "scan_dedup", "epq_self_modified", "epq_chain_pending_wait",
}

var s321Events atomic.Int64

func s321Note(site s321Site) {
	if !s321Enabled {
		return
	}
	s321Counters[site].Add(1)
	if s321Events.Add(1)%500 == 0 {
		s321Dump()
	}
}

// s321Dump writes the current tally to stderr. Called from the UPDATE path
// every 200 events so a run's log always ends with a usable snapshot.
func s321Dump() {
	if !s321Enabled {
		return
	}
	out := "S321:"
	for i := 0; i < int(s321NumSites); i++ {
		out += fmt.Sprintf(" %s=%d", s321Names[i], s321Counters[i].Load())
	}
	fmt.Fprintln(os.Stderr, out)
}
