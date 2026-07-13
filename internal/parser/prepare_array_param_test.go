package parser

import (
	"testing"
	"time"
)

// TestPrepareArrayParamTypeTerminates pins the parsePrepare fix: a typed
// parameter list with an array type (PREPARE f(regclass[]) AS ..., regress
// constraints.sql) previously spun forever in the param-type loop because
// parseTypeNameAfterCast fails on '[' without consuming it — the orphaned
// backend allocated "unknown" entries until the server OOMed.
func TestPrepareArrayParamTypeTerminates(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := Parse(`PREPARE get_nnconstraint_info(regclass[]) AS SELECT 1`)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Logf("parse returned err (acceptable if terminating): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parser hung on PREPARE name(type[]) — infinite loop")
	}
}
