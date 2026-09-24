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
// transition: G8 forbids a second one. Default ON since the M0145-0008
// cutover: every value gate measures the jointree pipeline, and
// GOOPG_JOINTREE_PIPELINE=0 selects the legacy pipeline until the
// legacy-deletion slices remove it together with this knob.
var jointreePipeline = jointreePipelineFromEnv(os.Getenv("GOOPG_JOINTREE_PIPELINE"))

// jointreePipelineFromEnv resolves the knob. Only the literal "0" selects the
// legacy pipeline; unset and anything else keep the default, the
// GOOPG_PGSHAPED_DP convention for a default-on knob.
func jointreePipelineFromEnv(v string) bool { return v != "0" }

// SetJointreePipeline selects the pipeline from a label an operator would
// export as GOOPG_JOINTREE_PIPELINE, resolved through jointreePipelineFromEnv,
// and returns the restore. It is the same knob reached across the package
// boundary, not a second selection mechanism (owner decision 2026-09-24,
// M0145-0008): executor tests of legacy-only machinery pin "0" with it until
// the legacy-deletion slices delete them with the code they cover.
// Process-global, like SetGatherPathsMode: the caller must run the restore.
func SetJointreePipeline(label string) (restore func()) {
	prev := jointreePipeline
	jointreePipeline = jointreePipelineFromEnv(label)
	return func() { jointreePipeline = prev }
}

// planSelectJointreePipeline is the jointree-first pipeline's per-scope
// entry point, dispatched from planSelectWithSettings — every subquery,
// CTE body and set-op branch re-enters here, the way PG's subquery_planner
// recurses per Query.
//
// The pipeline shares planSelectImpl's statement body with the legacy
// arm and diverges only at the M0145 stage sites. M0145-0003 landed the
// first: the WHERE arm runs pullUpSublinksIntoJointree
// (jointreepullup.go) — PG's pull_up_sublinks — so EXISTS/NOT-EXISTS
// bodies with flat jointrees enter the join-order problem as leaf
// entries + SpecialJoinInfo instead of the chain-splice's synthetic
// links. Stages land in order: 0003 sublink pull-up, 0004 appendrel,
// 0005 single-pass DP, 0006 upper-rel pathlists, 0007 unified lowering;
// the knob retires at 0008.
func planSelectJointreePipeline(s *parser.SelectStmt, cat catalog.Catalog, plannerSet PlannerSettings, scope *rtableScope) (Node, error) {
	return planSelectImpl(s, cat, plannerSet, scope, true)
}
