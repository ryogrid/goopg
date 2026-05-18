package pgcluster

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// segvBacktraceSource is the embedded C source for the SIGSEGV backtrace
// LD_PRELOAD shim. Kept in lockstep with tools/segv_backtrace/segv_backtrace.c
// — the Step-3dd regression test
// (TestSegvBacktraceSourceMatchesToolsCopy) pins byte-equality so the two
// copies cannot drift silently.
//
//go:embed segv_backtrace_src.txt
var segvBacktraceSource []byte

// segvBacktraceEnvName is the gate env var. The shim is opt-in because
// LD_PRELOAD'ing arbitrary helpers into PG processes is a diagnostic tool,
// not a production-mode default.
const segvBacktraceEnvName = "GOOPG_TEST_SEGV_BACKTRACE"

// segvBacktraceLDPreload returns the value that should be appended to
// LD_PRELOAD for the next exec.Command, or ("", false, nil) if the gate
// env var is not set. Build/probe errors degrade gracefully: the function
// returns ("", false, err) and the caller logs a single-line warning and
// proceeds without LD_PRELOAD.
func segvBacktraceLDPreload() (string, bool, error) {
	if os.Getenv(segvBacktraceEnvName) != "1" {
		return "", false, nil
	}
	soPath, err := ensureSegvBacktraceSO()
	if err != nil {
		return "", false, err
	}
	return soPath, true, nil
}

// ensureSegvBacktraceSO builds libsegv_backtrace.so from the embedded
// source into a content-addressed cache path under os.TempDir() and
// returns the absolute path. Build is skipped if a matching .so already
// exists for the current source hash.
func ensureSegvBacktraceSO() (string, error) {
	if override := os.Getenv("GOOPG_SEGV_BACKTRACE_SO"); override != "" {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
	}
	sum := sha256.Sum256(segvBacktraceSource)
	hash := hex.EncodeToString(sum[:])[:16]
	cacheDir := filepath.Join(os.TempDir(), "goopg-segv-backtrace")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("segv_backtrace: mkdir cache: %w", err)
	}
	soPath := filepath.Join(cacheDir, "libsegv_backtrace_"+hash+".so")
	if _, err := os.Stat(soPath); err == nil {
		return soPath, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("segv_backtrace: stat cache: %w", err)
	}
	srcPath := filepath.Join(cacheDir, "segv_backtrace_"+hash+".c")
	if err := os.WriteFile(srcPath, segvBacktraceSource, 0o644); err != nil {
		return "", fmt.Errorf("segv_backtrace: write source: %w", err)
	}
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	cmd := exec.Command(cc, "-shared", "-fPIC", "-O0", "-g", "-o", soPath, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("segv_backtrace: compile failed: %v\n%s", err, out)
	}
	return soPath, nil
}
