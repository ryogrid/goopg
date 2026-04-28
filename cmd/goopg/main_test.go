package main

import (
	"bytes"
	"os"
	"path/filepath"
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

// TestSubcommandStubsAreReachable confirms every still-stubbed subcommand
// dispatches without panicking. Stubs return exit code 1 ("not yet
// implemented"); `version` returns 0. `start` is excluded because it
// runs a real server (see internal/server tests); `init` is excluded
// because it now writes a real data directory and is covered by
// TestInitCommandLaysOutDataDir.
func TestSubcommandStubsAreReachable(t *testing.T) {
	cases := map[string]int{
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

// TestInitCommandLaysOutDataDir drives `goopg init -D <tmp>` and
// verifies the load-bearing files land under the chosen path.
// The detailed layout assertions live in internal/initdb; this is
// a thin CLI integration test so a regression in argument parsing
// or in the CLI→initdb wiring surfaces here.
func TestInitCommandLaysOutDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "-D", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"PG_VERSION", "postgresql.conf", "pg_hba.conf", "base/1", "pg_wal", "pg_xact", "global"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %q: %v", want, err)
		}
	}
}

// TestInitCommandRequiresD: invoking without -D should exit 2 with
// a clear diagnostic, matching the rest of the CLI's flag-error
// convention.
func TestInitCommandRequiresD(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-D") {
		t.Errorf("stderr=%q want a -D diagnostic", stderr.String())
	}
}
