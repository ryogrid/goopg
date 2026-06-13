package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/config"
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

// TestSubcommandStubsAreReachable confirms every subcommand
// dispatches without panicking. The exit codes here are what
// each subcommand returns when invoked with NO arguments:
//   - stop/reload/status all require -D; missing flag is exit 2.
//   - restart is still a stub returning exit 1.
//   - version always returns 0.
//
// `start` is excluded because it runs a real server (see
// internal/server tests); `init` is excluded because it now writes
// a real data directory and is covered by TestInitCommandLaysOutDataDir.
func TestSubcommandStubsAreReachable(t *testing.T) {
	cases := map[string]int{
		"stop":    2,
		"restart": 1,
		"reload":  2,
		"status":  2,
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

// TestInitCommandSeedsGUCs drives the full 001_initdb.pl "successful
// creation" option set through the CLI: --no-sync --text-search-config
// german --set default_text_search_config=german. The -c override must
// win, leaving an unquoted 'german' in postgresql.conf.
func TestInitCommandSeedsGUCs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"init", "-D", dir, "--no-sync",
		"--text-search-config", "german",
		"--set", "default_text_search_config=german",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "postgresql.conf"))
	if err != nil {
		t.Fatalf("read postgresql.conf: %v", err)
	}
	if !strings.Contains(string(data), "\ndefault_text_search_config = german") {
		t.Errorf("postgresql.conf missing seeded default_text_search_config; got:\n%s",
			grepLines(string(data), "default_text_search_config"))
	}
}

// TestInitCommandSetRequiresValue: a --set without '=' exits 2 with
// initdb's "requires a value" wording, and lays out nothing.
func TestInitCommandSetRequiresValue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var stdout, stderr bytes.Buffer
	code := run([]string{"init", "-D", dir, "--set", "bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "requires a value") {
		t.Errorf("stderr=%q want a 'requires a value' diagnostic", stderr.String())
	}
}

// grepLines returns the lines of s containing substr, for test diagnostics.
func grepLines(s, substr string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// TestPoolSlotsFromGUC pins the postgresql.conf -> shared_buffers ->
// PoolSlots wiring so a future loop can't silently drop the override.
// Default 128MB matches upstream PostgreSQL boot. 32 MB / 8 KB per
// page = 4096 slots; the conversion is exercised end-to-end via the
// canonical KB display form.
func TestPoolSlotsFromGUC(t *testing.T) {
	cases := []struct {
		name string
		set  string // value to Set; empty means leave at boot default
		want int
	}{
		{name: "boot default 128MB", want: 16384},
		{name: "32MB override", set: "32MB", want: 4096},
		{name: "256MB override", set: "256MB", want: 32768},
		{name: "8MB override", set: "8MB", want: 1024},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := config.BuildDefaultRegistry()
			if c.set != "" {
				if err := r.Set("shared_buffers", c.set, config.SourceConfigFile); err != nil {
					t.Fatalf("Set shared_buffers=%q: %v", c.set, err)
				}
			}
			got := poolSlotsFromGUC(r)
			if got != c.want {
				t.Errorf("poolSlotsFromGUC = %d, want %d", got, c.want)
			}
		})
	}
}

func TestPoolSlotsFromGUC_NilRegistry(t *testing.T) {
	if got := poolSlotsFromGUC(nil); got != 0 {
		t.Errorf("nil registry: got %d, want 0 (Open uses default)", got)
	}
}

func TestParsePrimaryConninfoFull(t *testing.T) {
	addr, appName, user := parsePrimaryConninfoFull(
		"host=127.0.0.1 port=5544 user=ryo dbname=postgres application_name=standby_a")
	if addr != "127.0.0.1:5544" {
		t.Fatalf("addr = %q, want 127.0.0.1:5544", addr)
	}
	if appName != "standby_a" {
		t.Fatalf("appName = %q, want standby_a", appName)
	}
	if user != "ryo" {
		t.Fatalf("user = %q, want ryo", user)
	}

	addr, appName, user = parsePrimaryConninfoFull("host=127.0.0.1 application_name=standby_b")
	if addr != "127.0.0.1:5432" {
		t.Fatalf("default-port addr = %q, want 127.0.0.1:5432", addr)
	}
	if appName != "standby_b" {
		t.Fatalf("default-port appName = %q, want standby_b", appName)
	}
	if user != "" {
		t.Fatalf("default-port user = %q, want empty", user)
	}
}
