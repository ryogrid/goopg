package optimizer

import (
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// jointreePipeline selects the M0145 jointree-first planning pipeline
// process-wide (AGENT.md §"Plan-parity harness" G8). It is the sanctioned
// dual-pipeline migration mechanism — precedent: GOOPG_PGSHAPED_DP,
// GOOPG_UNNEST_PREDP — and the ONLY pipeline-selection knob for the
// transition: G8 forbids a second one. Default off: the legacy pipeline
// stays the value-gated path for the whole transition, so every gate
// (tpcds-sf025 sweep, tpch-spotcheck, acceptance arms) keeps measuring it
// while the new pipeline is built under the knob on private, EXPLAIN-only
// lanes. The knob is retired by M0145-0008's cutover — the deciding
// measurement is the knob-arm parity capture this dispatch enables
// (scripts/jointree-parity-capture.sh).
var jointreePipeline = jointreePipelineFromEnv(os.Getenv("GOOPG_JOINTREE_PIPELINE"))

// jointreePipelineFromEnv resolves the knob. Fail-closed: only the literal
// "1" engages the new pipeline; unset, "0" and anything unrecognised stay on
// the legacy pipeline — a typo must never silently arm the path the value
// gates do not cover (oneRelSearchFromEnv sets the precedent).
func jointreePipelineFromEnv(v string) bool { return v == "1" }

// planSelectJointreePipeline is the jointree-first pipeline's per-scope
// entry point, dispatched from planSelectWithSettings — every subquery,
// CTE body and set-op branch re-enters here, the way PG's subquery_planner
// recurses per Query.
//
// M0145-0002 is the measurement harness only. Until M0145-0003 lands the
// jointree IR and pull-up (docs/design/0100-0149/
// m0145-0001-jointree-ir-and-lowering-contract.md), the entry delegates to
// the legacy pipeline, so a GOOPG_JOINTREE_PIPELINE=1 capture is a
// byte-for-byte A/A baseline proving the harness itself measures no
// movement. Stages land in order: 0003 sublink pull-up, 0004 appendrel,
// 0005 single-pass DP, 0006 upper-rel pathlists, 0007 unified lowering;
// the knob retires at 0008.
func planSelectJointreePipeline(s *parser.SelectStmt, cat catalog.Catalog, plannerSet PlannerSettings, scope *rtableScope) (Node, error) {
	return planSelectLegacyPipeline(s, cat, plannerSet, scope)
}
