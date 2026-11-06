package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/utils/misc"
)

// TestStartupReloadAppliesSIGHUP pins goopg start's startup-phase SIGHUP
// handling: before the server's own handler exists — crash and archive
// recovery — a SIGHUP neither terminates the process (Go's default action;
// the test binary would die) nor reloads concurrently; it flags a reload
// that process applies at the next check, postgresql.conf then
// postgresql.auto.conf, as ProcessStartupProcInterrupts runs
// ProcessConfigFile(PGC_SIGHUP).
func TestStartupReloadAppliesSIGHUP(t *testing.T) {
	dataDir := t.TempDir()
	confPath := filepath.Join(dataDir, "postgresql.conf")
	writeConf := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConf(confPath, "restore_command = 'cp old/%f %p'\n")
	registry := misc.BuildDefaultRegistry()
	entries, err := misc.ParseConfigFile(confPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyConfigEntries(entries); err != nil {
		t.Fatal(err)
	}
	restoreCommand := func() string { return stringGUC(registry, "restore_command", "") }

	r := newStartupReload(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer r.stop()
	sighup := func() {
		t.Helper()
		if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !r.pending.Load() {
			if time.Now().After(deadline) {
				t.Fatal("SIGHUP was not flagged")
			}
			time.Sleep(time.Millisecond)
		}
	}

	writeConf(confPath, "restore_command = 'cp new/%f %p'\n")
	r.process(registry, confPath, dataDir)
	if got := restoreCommand(); got != "cp old/%f %p" {
		t.Fatalf("restore_command = %q before any SIGHUP, want the boot value", got)
	}
	sighup()
	if got := restoreCommand(); got != "cp old/%f %p" {
		t.Fatalf("restore_command = %q: reload applied outside process", got)
	}
	r.process(registry, confPath, dataDir)
	if got := restoreCommand(); got != "cp new/%f %p" {
		t.Fatalf("restore_command = %q after SIGHUP, want the reloaded value", got)
	}

	// postgresql.auto.conf is read after postgresql.conf, so ALTER SYSTEM wins.
	writeConf(filepath.Join(dataDir, misc.AutoConfFileName), "restore_command = 'cp auto/%f %p'\n")
	sighup()
	r.process(registry, confPath, dataDir)
	if got := restoreCommand(); got != "cp auto/%f %p" {
		t.Fatalf("restore_command = %q, want postgresql.auto.conf's value", got)
	}

	// A file that fails to parse keeps the old values.
	writeConf(confPath, "restore_command = 'unterminated\n")
	sighup()
	r.process(registry, confPath, dataDir)
	if got := restoreCommand(); got != "cp auto/%f %p" {
		t.Fatalf("restore_command = %q after a malformed reload, want the old value", got)
	}
}
