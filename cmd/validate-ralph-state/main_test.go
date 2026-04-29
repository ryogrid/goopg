package main

import (
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	now := "2026-04-30T00:00:00Z"
	later := "2026-04-30T00:03:00Z"

	tests := []struct {
		name         string
		status       statusFile
		progress     progressFile
		wantIssueSub string
		wantNoIssues bool
	}{
		{
			name: "consistent running in_progress",
			status: statusFile{
				Status:            "running",
				LastAction:        "executing",
				LoopCount:         10,
				CallsMadeThisHour: 2,
				MaxCallsPerHour:   100,
				Timestamp:         now,
			},
			progress:     progressFile{Status: "in_progress", Timestamp: now},
			wantNoIssues: true,
		},
		{
			name: "running completed mismatch",
			status: statusFile{
				Status:            "running",
				LastAction:        "executing",
				LoopCount:         10,
				CallsMadeThisHour: 2,
				MaxCallsPerHour:   100,
				Timestamp:         now,
			},
			progress:     progressFile{Status: "completed", Timestamp: now},
			wantIssueSub: "running while progress.status is completed",
		},
		{
			name: "not running but in_progress",
			status: statusFile{
				Status:            "stopped",
				LastAction:        "idle",
				LoopCount:         10,
				CallsMadeThisHour: 2,
				MaxCallsPerHour:   100,
				Timestamp:         now,
			},
			progress:     progressFile{Status: "in_progress", Timestamp: now},
			wantIssueSub: "progress.status indicates running/in_progress",
		},
		{
			name: "executing but not running",
			status: statusFile{
				Status:            "stopped",
				LastAction:        "executing",
				LoopCount:         10,
				CallsMadeThisHour: 2,
				MaxCallsPerHour:   100,
				Timestamp:         now,
			},
			progress:     progressFile{Status: "completed", Timestamp: now},
			wantIssueSub: "last_action is executing but status.status is not running",
		},
		{
			name: "calls exceed quota",
			status: statusFile{
				Status:            "running",
				LastAction:        "executing",
				LoopCount:         10,
				CallsMadeThisHour: 101,
				MaxCallsPerHour:   100,
				Timestamp:         now,
			},
			progress:     progressFile{Status: "in_progress", Timestamp: now},
			wantIssueSub: "calls_made_this_hour (101) exceeds max_calls_per_hour (100)",
		},
		{
			name: "running with stale status timestamp",
			status: statusFile{
				Status:            "running",
				LastAction:        "executing",
				LoopCount:         10,
				CallsMadeThisHour: 2,
				MaxCallsPerHour:   100,
				Timestamp:         now,
			},
			progress:     progressFile{Status: "in_progress", Timestamp: later},
			wantIssueSub: "newer than status.timestamp",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issues := validate(tc.status, tc.progress, 2*time.Minute)
			if tc.wantNoIssues {
				if len(issues) != 0 {
					t.Fatalf("expected no issues, got %v", issues)
				}
				return
			}
			if len(issues) == 0 {
				t.Fatalf("expected issue containing %q, got none", tc.wantIssueSub)
			}
			for _, issue := range issues {
				if strings.Contains(issue, tc.wantIssueSub) {
					return
				}
			}
			t.Fatalf("issues %v do not contain expected substring %q", issues, tc.wantIssueSub)
		})
	}
}

func TestParseTimestamp(t *testing.T) {
	valid := []string{
		"2026-04-30T00:00:00Z",
		"2026-04-30 00:00:00",
		"2026-04-30T00:00:00",
	}
	for _, raw := range valid {
		if _, err := parseTimestamp(raw); err != nil {
			t.Fatalf("parseTimestamp(%q): %v", raw, err)
		}
	}

	if _, err := parseTimestamp("not-a-time"); err == nil {
		t.Fatal("parseTimestamp(invalid) expected error, got nil")
	}
}

func TestAutoRepairStaleRunningStatus(t *testing.T) {
	status := statusFile{
		Status:            "running",
		LastAction:        "executing",
		LoopCount:         42,
		CallsMadeThisHour: 3,
		MaxCallsPerHour:   100,
		Timestamp:         "2026-04-30T00:00:00Z",
	}
	progress := progressFile{
		Status:    "completed",
		Timestamp: "2026-04-30T00:05:00Z",
	}

	repairedStatus, repairedProgress, repairs := autoRepair(status, progress, 2*time.Minute)
	if len(repairs) == 0 {
		t.Fatal("expected at least one repair")
	}
	if repairedStatus.Status != "completed" {
		t.Fatalf("expected status completed, got %q", repairedStatus.Status)
	}
	if repairedStatus.LastAction != "idle" {
		t.Fatalf("expected last_action idle, got %q", repairedStatus.LastAction)
	}
	if repairedProgress != progress {
		t.Fatalf("expected progress unchanged, got %+v", repairedProgress)
	}

	issues := validate(repairedStatus, repairedProgress, 2*time.Minute)
	if len(issues) != 0 {
		t.Fatalf("expected no issues after repair, got %v", issues)
	}
}

func TestAutoRepairNoopWhenNotStale(t *testing.T) {
	status := statusFile{
		Status:            "running",
		LastAction:        "executing",
		LoopCount:         42,
		CallsMadeThisHour: 3,
		MaxCallsPerHour:   100,
		Timestamp:         "2026-04-30T00:00:00Z",
	}
	progress := progressFile{
		Status:    "completed",
		Timestamp: "2026-04-30T00:00:30Z",
	}

	repairedStatus, repairedProgress, repairs := autoRepair(status, progress, 2*time.Minute)
	if len(repairs) != 0 {
		t.Fatalf("expected no repairs, got %v", repairs)
	}
	if repairedStatus != status {
		t.Fatalf("expected status unchanged, got %+v", repairedStatus)
	}
	if repairedProgress != progress {
		t.Fatalf("expected progress unchanged, got %+v", repairedProgress)
	}
}
