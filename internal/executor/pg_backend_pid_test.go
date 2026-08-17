package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/utils/activity"
	"github.com/goopg/goopg/internal/optimizer"
)

// pg_backend_pid() must return the integer PID of the server process attached
// to the current session — resolved from the activity registry (the value
// reported in BackendKeyData and joined by pg_stat_activity.pid /
// pg_cancel_backend), falling back to the goopg.backend_pid GUC. It was
// entirely missing before M0118-0008, hard-failing with
// "function pg_backend_pid does not exist" (detach-partition-concurrently-3/4
// step s2snitch: INSERT INTO d_pid SELECT pg_backend_pid()).
func TestPgBackendPID(t *testing.T) {
	call := &optimizer.FuncCall{Name: "pg_backend_pid"}

	// Primary path: activity registry keyed by ProcNum.
	reg := activity.NewRegistry()
	procNum := reg.RegisterBackground(0, &activity.Backend{PID: "4242", BackendType: "client_backend", State: "active"})
	ctx := &Context{Activity: reg, ProcNum: procNum}
	got, err := evalFuncCall(call, nil, ctx)
	if err != nil {
		t.Fatalf("evalFuncCall(pg_backend_pid): %v", err)
	}
	if got.Kind != KindInt {
		t.Fatalf("pg_backend_pid kind = %v, want KindInt", got.Kind)
	}
	if got.Int != 4242 {
		t.Errorf("pg_backend_pid (registry) = %d, want 4242", got.Int)
	}

	// Fallback path: no activity registry, GUC carries the PID.
	ctxGUC := &Context{GetSetting: func(name string) (string, bool) {
		if name == "goopg.backend_pid" {
			return "7", true
		}
		return "", false
	}}
	got, err = evalFuncCall(call, nil, ctxGUC)
	if err != nil {
		t.Fatalf("evalFuncCall(pg_backend_pid) fallback: %v", err)
	}
	if got.Int != 7 {
		t.Errorf("pg_backend_pid (GUC fallback) = %d, want 7", got.Int)
	}

	// No source wired: returns 0, never errors (PG pg_backend_pid is never NULL).
	got, err = evalFuncCall(call, nil, &Context{})
	if err != nil {
		t.Fatalf("evalFuncCall(pg_backend_pid) empty: %v", err)
	}
	if got.Int != 0 {
		t.Errorf("pg_backend_pid (empty) = %d, want 0", got.Int)
	}
}
