package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestExecutePLpgSQLTriggerBody_NonPlpgsqlPassThrough verifies that a trigger
// function goopg cannot execute (e.g. LANGUAGE C) does NOT suppress the row it
// fires on: executePLpgSQLTriggerBody must return (nil, false, nil) — nil row,
// ok==false (fireTriggers' `!ok → continue` pass-through), nil error. ok==true
// with a nil row would be read by fireTriggers as "RETURN NULL → suppress".
// M0134-0078 B7; docs/design/m0134-0078-non-plpgsql-trigger-suppression.md.
func TestExecutePLpgSQLTriggerBody_NonPlpgsqlPassThrough(t *testing.T) {
	// The non-plpgsql arm returns before touching trig/ctx, so nil args are safe.
	row, ok, err := executePLpgSQLTriggerBody(&catalog.Routine{Language: "c"}, nil, nil)
	if err != nil {
		t.Fatalf("non-plpgsql trigger must not error, got %v", err)
	}
	if ok {
		t.Fatal("non-plpgsql trigger must pass through (ok==false), got ok==true — this suppresses the row")
	}
	if row != nil {
		t.Fatalf("non-plpgsql trigger must return a nil row, got %#v", row)
	}
}
