package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestNoArgsPrintsUsage guards the contract from
// .ralph/fix_plan.md M0: the binary builds and exits 0 on no-args/help.
func TestNoArgsPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("stdout missing usage banner: %q", stdout.String())
	}
}

func TestHelpFlagsExitZero(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{arg}, &stdout, &stderr); code != 0 {
			t.Errorf("run(%q) exit code = %d, want 0", arg, code)
		}
	}
}

func TestUnknownCommandExitsTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"frobnicate"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr missing diagnostic: %q", stderr.String())
	}
}

func TestVersionPrintsAndExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "goopg ") {
		t.Fatalf("stdout = %q, want goopg-prefixed version", stdout.String())
	}
}

// TestSubcommandStubsAreReachable confirms every advertised subcommand
// dispatches without panicking. Stubs return exit code 1 ("not yet
// implemented"); only `version` returns 0.
func TestSubcommandStubsAreReachable(t *testing.T) {
	cases := map[string]int{
		"init":    1,
		"start":   1,
		"stop":    1,
		"restart": 1,
		"reload":  1,
		"status":  1,
		"version": 0,
	}
	for cmd, want := range cases {
		var stdout, stderr bytes.Buffer
		got := run([]string{cmd}, &stdout, &stderr)
		if got != want {
			t.Errorf("run(%q) = %d, want %d (stderr=%q)", cmd, got, want, stderr.String())
		}
	}
}
