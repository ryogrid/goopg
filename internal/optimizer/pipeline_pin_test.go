package optimizer

import "testing"

// pinLegacyPipeline runs the calling test on the LEGACY planning pipeline
// (GOOPG_JOINTREE_PIPELINE=0) and restores the process-wide selection when the
// test ends.
//
// M0145-0008 cutover: the tests that call this exercise machinery only the
// legacy pipeline runs — the post-hoc unnest rewrites (`unnestSubqueriesInPlan`
// and its key/SJInfo/FlattenedRHS stamping), the pinned semi/anti spine seam,
// the rule-based join construction and the "knob off" inertness guards. They
// pinned the default arm implicitly while legacy WAS the default; pinning it
// explicitly keeps them measuring the code they were written for when the
// default flips, and marks them for the legacy-deletion slice, which deletes or
// rewrites them together with the code they cover.
func pinLegacyPipeline(t testing.TB) {
	t.Helper()
	prev := jointreePipeline
	jointreePipeline = false
	t.Cleanup(func() { jointreePipeline = prev })
}
