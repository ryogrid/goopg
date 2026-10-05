package optimizer

import "testing"

// enableScalarUnnestForTest turns on the scalar-sublink decorrelation
// (GOOPG_SCALAR_UNNEST, default off since M0145-0008y) for one test, so the
// tests that pin that machinery's coordinates and guards keep exercising it.
func enableScalarUnnestForTest(t *testing.T) {
	t.Helper()
	prev := scalarUnnestOn.Load()
	scalarUnnestOn.Store(true)
	t.Cleanup(func() { scalarUnnestOn.Store(prev) })
}

// TestScalarSublinkStaysSubPlanByDefault pins M0145-0008y: PG keeps every
// correlated scalar sublink a SubPlan (pull_up_sublinks converts only
// ANY/EXISTS), so with the default switch canUnnestSubquery refuses even a
// shape it would decorrelate when GOOPG_SCALAR_UNNEST=on.
func TestScalarSublinkStaysSubPlanByDefault(t *testing.T) {
	if scalarUnnestFromEnv("") {
		t.Fatal("GOOPG_SCALAR_UNNEST must default off")
	}
	if !scalarUnnestFromEnv("on") {
		t.Fatal("GOOPG_SCALAR_UNNEST=on must restore the decorrelation")
	}
}
