package cluster

import (
	"testing"
	"time"
)

// TestStartupWaitFromEnv pins the PGCTLTIMEOUT start budget (M-NIGHTLY
// AI-20261023-005128-001): pg_ctl's environment override raises a normal
// budget (at least the 20 s default) to the given seconds, never lowers one,
// ignores a malformed or non-positive value, and leaves a deliberately short
// budget — a test provoking a start timeout — alone.
func TestStartupWaitFromEnv(t *testing.T) {
	for _, tc := range []struct {
		budget time.Duration
		env    string
		want   time.Duration
	}{
		{20 * time.Second, "", 20 * time.Second},
		{20 * time.Second, "60", 60 * time.Second},
		{30 * time.Second, " 60 ", 60 * time.Second},
		{90 * time.Second, "60", 90 * time.Second},
		{20 * time.Second, "abc", 20 * time.Second},
		{20 * time.Second, "0", 20 * time.Second},
		{20 * time.Second, "-5", 20 * time.Second},
		{time.Second, "60", time.Second},
	} {
		if got := startupWaitFromEnv(tc.budget, tc.env); got != tc.want {
			t.Errorf("startupWaitFromEnv(%v, %q) = %v, want %v", tc.budget, tc.env, got, tc.want)
		}
	}
}

// TestNewHonoursPGCTLTIMEOUT: the override reaches a Cluster built with the
// default budget and with an explicit 20 s one, as the testport suites set.
func TestNewHonoursPGCTLTIMEOUT(t *testing.T) {
	t.Setenv("PGCTLTIMEOUT", "60")
	for _, wait := range []time.Duration{0, 20 * time.Second} {
		c, err := New("pgctltimeout", Options{RepoRoot: t.TempDir(), DataDir: t.TempDir(), StartupWait: wait})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.startupWait != 60*time.Second {
			t.Fatalf("StartupWait %v: startupWait = %v, want 60s", wait, c.startupWait)
		}
	}
}
