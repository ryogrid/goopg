package util

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCommandCapturesOutputAndExitCode(t *testing.T) {
	res, err := RunCommand(CommandSpec{
		Name: "sh",
		Args: []string{"-c", "echo out; echo err 1>&2; exit 7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode=%d want 7", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "out") {
		t.Fatalf("Stdout=%q missing out", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "err") {
		t.Fatalf("Stderr=%q missing err", res.Stderr)
	}
	if res.TimedOut {
		t.Fatal("TimedOut=true want false")
	}
}

func TestRunCommandTimeout(t *testing.T) {
	res, err := RunCommand(CommandSpec{
		Name:    "sh",
		Args:    []string{"-c", "sleep 2"},
		Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatal("TimedOut=false want true")
	}
}

func TestWriteAndScanLogFile(t *testing.T) {
	dir, err := MkdirTemp("goopg-testutil-util-")
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "server.log")
	if err := WriteTextFile(logPath, "line1\nline2\n", 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := FileContains(logPath, "line2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("FileContains=false want true")
	}
}

func TestWaitForFileContains(t *testing.T) {
	dir, err := MkdirTemp("goopg-testutil-util-")
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "async.log")
	if err := WriteTextFile(logPath, "booting\n", 0o644); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = WriteTextFile(logPath, "booting\nready\n", 0o644)
	}()

	if err := WaitForFileContains(logPath, "ready", 2*time.Second); err != nil {
		t.Fatal(err)
	}
}
