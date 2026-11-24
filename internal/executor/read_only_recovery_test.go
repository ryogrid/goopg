package executor

import (
	"errors"
	"testing"
)

// TestPreventCommandDuringRecovery: a hot standby refuses data-modifying
// statements with PG's read-only-transaction error (001_stream_rep expects
// psql exit 3 for INSERT on both standbys); a primary is unaffected.
// M0149-0077b.
func TestPreventCommandDuringRecovery(t *testing.T) {
	if err := preventCommandDuringRecovery(&Context{IsStandby: false}, "INSERT"); err != nil {
		t.Fatalf("primary: %v", err)
	}
	err := preventCommandDuringRecovery(&Context{IsStandby: true}, "INSERT")
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != "25006" || ee.Message != "cannot execute INSERT in a read-only transaction" {
		t.Fatalf("standby: %v", err)
	}
	if err := preventCommandDuringRecovery(nil, "DELETE"); err != nil {
		t.Fatalf("nil context: %v", err)
	}
}
