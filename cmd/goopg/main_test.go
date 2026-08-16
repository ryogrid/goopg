package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/control"
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
//   - stop/restart/reload/status all require -D; missing flag is exit 2.
//   - version always returns 0.
//
// `start` is excluded because it runs a real server (see
// internal/server tests); `init` is excluded because it now writes
// a real data directory and is covered by TestInitCommandLaysOutDataDir.
// `restart`'s stop-then-start orchestration is covered separately by
// TestRunRestartWithStarter (it can't run through the real `start` here
// without blocking on a foreground server).
func TestSubcommandStubsAreReachable(t *testing.T) {
	cases := map[string]int{
		"stop":    2,
		"restart": 2,
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

// TestRunRestartWithStarter exercises runRestart's stop-then-start
// orchestration via the injectable starter (runRestart itself always wires
// the real runStart, which blocks forever serving a foreground listener —
// not something a unit test can drive directly).
func TestRunRestartWithStarter(t *testing.T) {
	t.Run("missing -D exits 2 without starting", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		started := false
		code := runRestartWithStarter(nil, &stdout, &stderr, func(args []string, _, _ io.Writer) int {
			started = true
			return 0
		})
		if code != 2 {
			t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr.String())
		}
		if started {
			t.Fatal("starter invoked despite missing -D")
		}
	})

	t.Run("no postmaster.pid starts straight away with default listen addr", func(t *testing.T) {
		dir := t.TempDir()
		var stdout, stderr bytes.Buffer
		var gotArgs []string
		code := runRestartWithStarter([]string{"-D", dir}, &stdout, &stderr, func(args []string, _, _ io.Writer) int {
			gotArgs = args
			return 0
		})
		if code != 0 {
			t.Fatalf("exit=%d, want 0 (stderr=%q)", code, stderr.String())
		}
		want := []string{"-D", dir, "-listen", "127.0.0.1:5432"}
		if !slices.Equal(gotArgs, want) {
			t.Fatalf("starter args = %v, want %v", gotArgs, want)
		}
	})

	t.Run("stale pidfile is treated as not running", func(t *testing.T) {
		dir := t.TempDir()
		// A pid essentially guaranteed to be dead: fork a real child,
		// wait for it to exit, then reuse its (now-stale) pid number.
		cmd := exec.Command("true")
		if err := cmd.Run(); err != nil {
			t.Fatalf("spawn throwaway process: %v", err)
		}
		deadPID := cmd.Process.Pid
		if err := control.WritePIDFile(dir, control.PIDFile{
			PID:        deadPID,
			DataDir:    dir,
			StartedAt:  time.Now(),
			ListenAddr: "127.0.0.1:9999",
			SocketPath: filepath.Join(dir, control.SocketName),
		}); err != nil {
			t.Fatalf("WritePIDFile: %v", err)
		}

		var stdout, stderr bytes.Buffer
		var started bool
		code := runRestartWithStarter([]string{"-D", dir}, &stdout, &stderr, func(args []string, _, _ io.Writer) int {
			started = true
			return 0
		})
		if code != 0 {
			t.Fatalf("exit=%d, want 0 (stderr=%q)", code, stderr.String())
		}
		if !started {
			t.Fatal("starter not invoked for a stale pidfile")
		}
	})

	t.Run("live server is stopped before starting, reusing its listen addr", func(t *testing.T) {
		dir := t.TempDir()
		ln, err := control.NewListener(filepath.Join(dir, control.SocketName))
		if err != nil {
			t.Fatalf("NewListener: %v", err)
		}
		defer ln.Close()

		// A real, killable process to stand in for the "running server".
		sleeper := exec.Command("sleep", "60")
		if err := sleeper.Start(); err != nil {
			t.Fatalf("start sleeper: %v", err)
		}
		defer func() { _ = sleeper.Process.Kill(); _, _ = sleeper.Process.Wait() }()

		ln.OnStop = func() error {
			// Kill alone leaves a zombie until reaped, and
			// syscall.Kill(pid, 0) still succeeds against a zombie —
			// Wait so ProcessAlive's poll loop actually observes death.
			_ = sleeper.Process.Kill()
			_, _ = sleeper.Process.Wait()
			return nil
		}
		go ln.Serve()

		if err := control.WritePIDFile(dir, control.PIDFile{
			PID:        sleeper.Process.Pid,
			DataDir:    dir,
			StartedAt:  time.Now(),
			ListenAddr: "127.0.0.1:7777",
			SocketPath: ln.Path(),
		}); err != nil {
			t.Fatalf("WritePIDFile: %v", err)
		}

		var stdout, stderr bytes.Buffer
		var gotArgs []string
		code := runRestartWithStarter([]string{"-D", dir}, &stdout, &stderr, func(args []string, _, _ io.Writer) int {
			gotArgs = args
			return 0
		})
		if code != 0 {
			t.Fatalf("exit=%d, want 0 (stderr=%q)", code, stderr.String())
		}
		want := []string{"-D", dir, "-listen", "127.0.0.1:7777"}
		if !slices.Equal(gotArgs, want) {
			t.Fatalf("starter args = %v, want %v (should default to the stopped instance's own listen addr)", gotArgs, want)
		}
		if control.ProcessAlive(sleeper.Process.Pid) {
			t.Fatal("sleeper still alive after restart's stop phase")
		}
	})
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

// TestInitCommandAllowGroupAccess drives 001_initdb.pl's "successful creation
// with group access" through the CLI (initdb --allow-group-access <dir>) and
// asserts the resulting cluster satisfies check_mode_recursive(0750, 0640):
// every directory 0750, every file 0640, plus the seeded log_file_mode.
func TestInitCommandAllowGroupAccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data_group")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "-D", dir, "--allow-group-access"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if got := info.Mode().Perm(); got != 0o750 {
				t.Errorf("dir %q mode = %04o, want 0750", p, got)
			}
		case info.Mode().IsRegular():
			if got := info.Mode().Perm(); got != 0o640 {
				t.Errorf("file %q mode = %04o, want 0640", p, got)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "postgresql.conf"))
	if err != nil {
		t.Fatalf("read postgresql.conf: %v", err)
	}
	if !strings.Contains(string(data), "log_file_mode = 0640") {
		t.Errorf("postgresql.conf missing seeded `log_file_mode = 0640`; got:\n%s",
			grepLines(string(data), "log_file_mode"))
	}
}

// TestInitCommandDataChecksums drives the -k/--data-checksums and
// --no-data-checksums flags (upstream initdb -k) through the CLI and asserts
// pg_control's data_checksum_version reflects the requested mode. Matching
// upstream PG 18, goopg defaults checksums ON, so a plain `init` yields
// version 1; --no-data-checksums disables and overrides -k.
func TestInitCommandDataChecksums(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantVers uint32
	}{
		{"default-on", []string{}, 1},
		{"k-short", []string{"-k"}, 1},
		{"long", []string{"--data-checksums"}, 1},
		{"no-data-checksums", []string{"--no-data-checksums"}, 0},
		{"no-overrides-k", []string{"-k", "--no-data-checksums"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			var stdout, stderr bytes.Buffer
			args := append([]string{"init", "-D", dir, "--no-sync"}, tc.args...)
			if code := run(args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			ctrl, err := control.ReadControlFile(dir)
			if err != nil {
				t.Fatalf("ReadControlFile: %v", err)
			}
			if ctrl.DataChecksumVersion != tc.wantVers {
				t.Fatalf("data_checksum_version = %d, want %d", ctrl.DataChecksumVersion, tc.wantVers)
			}
		})
	}
}

// TestInitCommandSyncMethodAndNoSyncDataFiles drives 001_initdb.pl's
// `initdb --sync-only [--no-sync-data-files] [--sync-method=syncfs]` tier
// through the CLI: a previously laid-out cluster is re-synced via each
// option and must exit 0. A bogus --sync-method exits 1 with the
// "unrecognized sync method" diagnostic.
func TestInitCommandSyncMethodAndNoSyncDataFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var stdout, stderr bytes.Buffer
	// Lay out a cluster first (no fsync for speed).
	if code := run([]string{"init", "-D", dir, "--no-sync"}, &stdout, &stderr); code != 0 {
		t.Fatalf("initial init exit=%d stderr=%q", code, stderr.String())
	}

	// --sync-only --no-sync-data-files (skips the base/ subtree).
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"init", "-D", dir, "--sync-only", "--no-sync-data-files"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--sync-only --no-sync-data-files exit=%d stderr=%q", code, stderr.String())
	}

	// --sync-only --sync-method=syncfs (Linux only).
	stdout.Reset()
	stderr.Reset()
	code := run([]string{"init", "-D", dir, "--sync-only", "--sync-method", "syncfs"}, &stdout, &stderr)
	if runtime.GOOS == "linux" {
		if code != 0 {
			t.Fatalf("--sync-method=syncfs on linux exit=%d stderr=%q", code, stderr.String())
		}
	} else if code == 0 {
		t.Fatalf("--sync-method=syncfs should fail on %s", runtime.GOOS)
	}

	// Bogus method is rejected with the upstream wording.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"init", "-D", dir, "--sync-only", "--sync-method", "bogus"}, &stdout, &stderr); code != 1 {
		t.Fatalf("--sync-method=bogus exit=%d, want 1 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unrecognized sync method") {
		t.Errorf("stderr=%q want an 'unrecognized sync method' diagnostic", stderr.String())
	}
}

// TestInitCommandAuthAndPwfile checks that -A/--auth-host/--auth-local and
// --pwfile thread through to Init: the resolved methods land in pg_hba.conf,
// and a password method without --pwfile is rejected.
func TestInitCommandAuthAndPwfile(t *testing.T) {
	base := t.TempDir()
	var stdout, stderr bytes.Buffer

	// -A scram-sha-256 sets both sides; --pwfile satisfies check_need_password.
	dir := filepath.Join(base, "data1")
	pwPath := filepath.Join(base, "pw.txt")
	if err := os.WriteFile(pwPath, []byte("sekret\n"), 0o600); err != nil {
		t.Fatalf("write pwfile: %v", err)
	}
	if code := run([]string{"init", "-D", dir, "--no-sync", "-A", "scram-sha-256", "--pwfile", pwPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("-A scram --pwfile exit=%d stderr=%q", code, stderr.String())
	}
	hba, err := os.ReadFile(filepath.Join(dir, "pg_hba.conf"))
	if err != nil {
		t.Fatalf("read pg_hba.conf: %v", err)
	}
	if !strings.Contains(string(hba), "127.0.0.1/32    scram-sha-256") {
		t.Errorf("pg_hba.conf missing scram host rule:\n%s", grepLines(string(hba), "all"))
	}
	if !strings.Contains(string(hba), "local    all       all                    scram-sha-256") {
		t.Errorf("pg_hba.conf missing scram local rule:\n%s", grepLines(string(hba), "local"))
	}

	// --auth-host overrides only the host side; local stays the -A value.
	stdout.Reset()
	stderr.Reset()
	dir2 := filepath.Join(base, "data2")
	if code := run([]string{"init", "-D", dir2, "--no-sync", "-A", "trust", "--auth-host", "reject"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--auth-host override exit=%d stderr=%q", code, stderr.String())
	}
	hba2, err := os.ReadFile(filepath.Join(dir2, "pg_hba.conf"))
	if err != nil {
		t.Fatalf("read pg_hba.conf #2: %v", err)
	}
	if !strings.Contains(string(hba2), "127.0.0.1/32    reject") {
		t.Errorf("--auth-host=reject not reflected:\n%s", grepLines(string(hba2), "127.0.0.1"))
	}
	if !strings.Contains(string(hba2), "local    all       all                    trust") {
		t.Errorf("local side should stay trust:\n%s", grepLines(string(hba2), "local"))
	}

	// Password method without --pwfile is rejected (exit 1).
	stdout.Reset()
	stderr.Reset()
	dir3 := filepath.Join(base, "data3")
	if code := run([]string{"init", "-D", dir3, "--no-sync", "-A", "md5"}, &stdout, &stderr); code != 1 {
		t.Fatalf("-A md5 without --pwfile exit=%d, want 1 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "must specify a password") {
		t.Errorf("stderr=%q want a 'must specify a password' diagnostic", stderr.String())
	}
}

// TestInitCommandEncoding drives -E/--encoding through the CLI: a valid name
// (in initdb's punctuation-insensitive form) lays out a cluster, while an
// unknown or client-only encoding is rejected (exit 1) with initdb's exact
// "is not a valid server encoding name" wording and lays out nothing. The
// byte-level pg_database.encoding wiring is pinned in the initdb package.
func TestInitCommandEncoding(t *testing.T) {
	base := t.TempDir()

	// Valid server encoding via the long form, punctuation-insensitive.
	var stdout, stderr bytes.Buffer
	dir := filepath.Join(base, "ok")
	if code := run([]string{"init", "-D", dir, "--no-sync", "--encoding", "LATIN1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--encoding LATIN1 exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "PG_VERSION")); err != nil {
		t.Errorf("cluster not laid out: %v", err)
	}

	// Client-only encoding (SJIS): recognized but rejected as a server encoding.
	stdout.Reset()
	stderr.Reset()
	bad := filepath.Join(base, "client-only")
	if code := run([]string{"init", "-D", bad, "--no-sync", "-E", "SJIS"}, &stdout, &stderr); code != 1 {
		t.Fatalf("-E SJIS exit=%d, want 1 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "is not a valid server encoding name") {
		t.Errorf("stderr=%q want a 'not a valid server encoding name' diagnostic", stderr.String())
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Errorf("rejected encoding should lay out nothing, but %q exists (err=%v)", bad, err)
	}

	// Unknown encoding name is likewise rejected.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"init", "-D", filepath.Join(base, "unknown"), "--no-sync", "-E", "bogus"}, &stdout, &stderr); code != 1 {
		t.Fatalf("-E bogus exit=%d, want 1 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "is not a valid server encoding name") {
		t.Errorf("stderr=%q want a 'not a valid server encoding name' diagnostic", stderr.String())
	}
}

// TestInitCommandLocaleProvider exercises the --locale-provider / --locale /
// --lc-* / --builtin-locale option family through the CLI, mirroring the
// non-ICU locale cases of upstream initdb's 001_initdb.pl.
func TestInitCommandLocaleProvider(t *testing.T) {
	base := t.TempDir()

	// builtin provider with --locale C succeeds and lays out a cluster.
	var stdout, stderr bytes.Buffer
	ok := filepath.Join(base, "builtin-ok")
	if code := run([]string{"init", "-D", ok, "--no-sync", "--locale-provider", "builtin", "--locale", "C"}, &stdout, &stderr); code != 0 {
		t.Fatalf("builtin --locale C exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(ok, "PG_VERSION")); err != nil {
		t.Errorf("cluster not laid out: %v", err)
	}

	// builtin C.UTF-8 with --encoding UTF-8 succeeds.
	stdout.Reset()
	stderr.Reset()
	utf8dir := filepath.Join(base, "builtin-cutf8")
	if code := run([]string{"init", "-D", utf8dir, "--no-sync", "--locale-provider", "builtin", "--encoding", "UTF-8", "--lc-collate", "C", "--lc-ctype", "C", "--builtin-locale", "C.UTF-8"}, &stdout, &stderr); code != 0 {
		t.Fatalf("builtin C.UTF-8 + UTF-8 exit=%d stderr=%q", code, stderr.String())
	}

	// Each rejection path: bad provider, builtin-without-locale, ICU
	// provider (no ICU build), libc+--icu-locale combo, and builtin C.UTF-8
	// with a non-UTF8 encoding. All must exit 1 and lay out nothing.
	rejects := []struct {
		name string
		args []string
		want string
	}{
		{"xyz", []string{"--locale-provider", "xyz"}, "unrecognized locale provider"},
		{"builtin-no-locale", []string{"--locale-provider", "builtin"}, "locale must be specified if provider is builtin"},
		{"icu-no-build", []string{"--locale-provider", "icu", "--icu-locale", "en"}, "ICU is not supported in this build"},
		{"libc-icu-combo", []string{"--locale-provider", "libc", "--icu-locale", "en"}, "--icu-locale cannot be specified"},
		{"builtin-cutf8-sqlascii", []string{"--locale-provider", "builtin", "--encoding", "SQL_ASCII", "--lc-collate", "C", "--lc-ctype", "C", "--builtin-locale", "C.UTF-8"}, "requires encoding"},
	}
	for _, c := range rejects {
		stdout.Reset()
		stderr.Reset()
		dir := filepath.Join(base, "rej-"+c.name)
		args := append([]string{"init", "-D", dir, "--no-sync"}, c.args...)
		if code := run(args, &stdout, &stderr); code != 1 {
			t.Errorf("%s: exit=%d, want 1 (stderr=%q)", c.name, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%s: stderr=%q, want containing %q", c.name, stderr.String(), c.want)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s: rejected init should lay out nothing, but %q exists (err=%v)", c.name, dir, err)
		}
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
			r := misc.BuildDefaultRegistry()
			if c.set != "" {
				if err := r.Set("shared_buffers", c.set, misc.SourceConfigFile); err != nil {
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

// TestTransactionBuffersFromGUC pins the postgresql.conf ->
// transaction_buffers -> OpenOptions.TransactionBuffers wiring so a future
// loop can't silently drop the override of the live CLOG SLRU pool size
// (M0117-0006 Part B follow-up). The GUC is a unit-less BLCKSZ-buffer count
// with boot default 0 (auto-tune to the 16-page bank floor); a non-zero value
// flows verbatim into Open and is clamped in EffectiveCLOGBuffers.
func TestTransactionBuffersFromGUC(t *testing.T) {
	cases := []struct {
		name string
		set  string // value to Set; empty means leave at boot default
		want int
	}{
		{name: "boot default auto-tune", want: 0},
		{name: "128 override", set: "128", want: 128},
		{name: "8 override (below floor, clamp deferred)", set: "8", want: 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := misc.BuildDefaultRegistry()
			if c.set != "" {
				if err := r.Set("transaction_buffers", c.set, misc.SourceConfigFile); err != nil {
					t.Fatalf("Set transaction_buffers=%q: %v", c.set, err)
				}
			}
			got := intGUC(r, "transaction_buffers", 0)
			if got != c.want {
				t.Errorf("intGUC(transaction_buffers) = %d, want %d", got, c.want)
			}
		})
	}
}

func TestTransactionBuffersFromGUC_NilRegistry(t *testing.T) {
	if got := intGUC(nil, "transaction_buffers", 0); got != 0 {
		t.Errorf("nil registry: got %d, want 0 (Open auto-tunes)", got)
	}
}

func TestParsePrimaryConninfoFull(t *testing.T) {
	addr, appName, user, sslmode := parsePrimaryConninfoFull(
		"host=127.0.0.1 port=5544 user=ryo dbname=postgres application_name=standby_a sslmode=require")
	if addr != "127.0.0.1:5544" {
		t.Fatalf("addr = %q, want 127.0.0.1:5544", addr)
	}
	if appName != "standby_a" {
		t.Fatalf("appName = %q, want standby_a", appName)
	}
	if user != "ryo" {
		t.Fatalf("user = %q, want ryo", user)
	}
	if sslmode != "require" {
		t.Fatalf("sslmode = %q, want require", sslmode)
	}

	addr, appName, user, sslmode = parsePrimaryConninfoFull("host=127.0.0.1 application_name=standby_b")
	if addr != "127.0.0.1:5432" {
		t.Fatalf("default-port addr = %q, want 127.0.0.1:5432", addr)
	}
	if appName != "standby_b" {
		t.Fatalf("default-port appName = %q, want standby_b", appName)
	}
	if user != "" {
		t.Fatalf("default-port user = %q, want empty", user)
	}
	if sslmode != "" {
		t.Fatalf("default sslmode = %q, want empty", sslmode)
	}
}
