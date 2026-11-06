package misc

import (
	"errors"
	"testing"
)

// TestParseRecoveryTargetTimeline pins check_recovery_target_timeline's
// parsing: "current"/"latest" by name, anything else through strtoul(s,
// NULL, 0) — base from the prefix, the longest digit prefix, 0 when there is
// none (accepted: PG then reports timeline 0 does not exist) — rejecting only
// an overflow, with PG's errdetail.
func TestParseRecoveryTargetTimeline(t *testing.T) {
	for _, c := range []struct {
		in   string
		goal RecoveryTargetTimelineGoal
		tli  uint32
		ok   bool
	}{
		{"latest", RecoveryTargetTimelineLatest, 0, true},
		{"current", RecoveryTargetTimelineCurrent, 0, true},
		{"2", RecoveryTargetTimelineNumeric, 2, true},
		{" 3", RecoveryTargetTimelineNumeric, 3, true},
		{"0x1f", RecoveryTargetTimelineNumeric, 31, true},
		{"010", RecoveryTargetTimelineNumeric, 8, true},
		{"7abc", RecoveryTargetTimelineNumeric, 7, true},
		{"abc", RecoveryTargetTimelineNumeric, 0, true},
		{"Latest", RecoveryTargetTimelineNumeric, 0, true},
		{"99999999999999999999999", RecoveryTargetTimelineNumeric, 0, false},
	} {
		goal, tli, ok := ParseRecoveryTargetTimeline(c.in)
		if ok != c.ok || (ok && (goal != c.goal || tli != c.tli)) {
			t.Errorf("%q: got (%d, %d, %v), want (%d, %d, %v)", c.in, goal, tli, ok, c.goal, c.tli, c.ok)
		}
	}

	r := BuildDefaultRegistry()
	err := r.Set("recovery_target_timeline", "99999999999999999999999", SourceConfigFile)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Msg != `invalid value for parameter "recovery_target_timeline": "99999999999999999999999"` ||
		ve.Detail != `"recovery_target_timeline" is not a valid number.` {
		t.Fatalf("Set overflow: err = %v (%+v)", err, ve)
	}
	if err := r.Set("recovery_target_timeline", "current", SourceConfigFile); err != nil {
		t.Fatalf("Set current: %v", err)
	}
}
