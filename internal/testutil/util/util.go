package util

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CommandSpec describes one external command execution.
type CommandSpec struct {
	Name    string
	Args    []string
	Dir     string
	Env     []string
	Stdin   string
	Timeout time.Duration
}

// CommandResult captures process outcome and outputs.
type CommandResult struct {
	ExitCode int
	TimedOut bool
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// MkdirTemp creates a temporary directory for tests.
func MkdirTemp(prefix string) (string, error) {
	return os.MkdirTemp("", prefix)
}

// WriteTextFile writes content to path, creating parent directories.
func WriteTextFile(path string, content string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), perm)
}

// FileContains reports whether filePath currently contains needle.
func FileContains(filePath, needle string) (bool, error) {
	b, err := os.ReadFile(filePath)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(b), needle), nil
}

// WaitForFileContains polls filePath until needle appears or timeout expires.
func WaitForFileContains(filePath, needle string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		ok, err := FileContains(filePath, needle)
		if err == nil && ok {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("wait for %q in %s: %w", needle, filePath, err)
			}
			return fmt.Errorf("wait for %q in %s: timeout after %s", needle, filePath, timeout)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// RunCommand executes one external command and captures stdout/stderr.
//
// Non-zero exit status is reported via CommandResult.ExitCode and does not
// return an error. Errors are returned only for setup/launch failures.
func RunCommand(spec CommandSpec) (CommandResult, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return CommandResult{}, errors.New("run command: Name is required")
	}

	ctx := context.Background()
	cancel := func() {}
	if spec.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	res := CommandResult{
		ExitCode: -1,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}

	if err == nil {
		res.ExitCode = 0
		return res, nil
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		if ee := new(exec.ExitError); errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		}
		return res, nil
	}

	if ee := new(exec.ExitError); errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}

	return res, err
}
