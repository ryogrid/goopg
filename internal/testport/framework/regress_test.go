package framework

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockRegressExec struct {
	outputs map[string]string
	errs    map[string]error
}

func (m *mockRegressExec) ExecuteSQL(_ context.Context, sql string) (string, error) {
	if err, ok := m.errs[sql]; ok {
		return "", err
	}
	if out, ok := m.outputs[sql]; ok {
		return out, nil
	}
	return "", errors.New("defer: not implemented")
}

func TestRunRegressSubsetReportsStatuses(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "postgres/src/test/regress/sql/pass_case.sql"), "SELECT 1;\n")
	mustWrite(t, filepath.Join(repo, "postgres/src/test/regress/expected/pass_case.out"), "1\n")
	mustWrite(t, filepath.Join(repo, "postgres/src/test/regress/sql/defer_case.sql"), "SELECT defer;\n")
	mustWrite(t, filepath.Join(repo, "postgres/src/test/regress/expected/defer_case.out"), "defer\n")
	mustWrite(t, filepath.Join(repo, "postgres/src/test/regress/sql/excluded_case.sql"), "SELECT excluded;\n")
	mustWrite(t, filepath.Join(repo, "postgres/src/test/regress/expected/excluded_case.out"), "excluded\n")

	cases, err := DiscoverRegressCases(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 3 {
		t.Fatalf("cases=%d want 3", len(cases))
	}

	exec := &mockRegressExec{
		outputs: map[string]string{
			"SELECT 1;\n": "1\n",
		},
		errs: map[string]error{
			"SELECT defer;\n":    ErrDeferred,
			"SELECT excluded;\n": ErrExcluded,
		},
	}
	results, err := RunRegressSubset(context.Background(), repo, cases, exec)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results=%d want 3", len(results))
	}

	got := map[string]string{}
	for _, r := range results {
		got[r.Name] = r.Status
	}
	if got["pass_case"] != "port" {
		t.Fatalf("pass_case status=%q want port", got["pass_case"])
	}
	if got["defer_case"] != "defer" {
		t.Fatalf("defer_case status=%q want defer", got["defer_case"])
	}
	if got["excluded_case"] != "excluded" {
		t.Fatalf("excluded_case status=%q want excluded", got["excluded_case"])
	}
}

// TestRunRegressSubsetTimeoutIsNotOutputMismatch pins the root-0029 fix: when
// the executor reports that the client was killed by the per-case deadline, the
// truncated output it returns must NOT be diffed against the expected file.
// Before the fix the partial output fell through to the diff and every wedged
// case reported "output mismatch; normalization rules need extension", which
// the nightly summarizer then filed as an independent regression — one wedged
// cluster produced 36 of them in run 20260725-011243.
func TestRunRegressSubsetTimeoutIsNotOutputMismatch(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "postgres/src/test/regress/sql/wedged_case.sql"), "SELECT pg_sleep(600);\n")
	mustWrite(t, filepath.Join(repo, "postgres/src/test/regress/expected/wedged_case.out"), "full expected output\n")

	cases, err := DiscoverRegressCases(repo)
	if err != nil {
		t.Fatal(err)
	}

	// The real ClusterRegressExecutor returns the partial psql output together
	// with the wrapped ErrExecTimeout; mirror that shape here.
	exec := &timeoutRegressExec{
		partial:  "SELECT pg_sleep(600);\n",
		timeoutE: fmt.Errorf("%w: psql killed after 2m0s", ErrExecTimeout),
	}
	results, err := RunRegressSubset(context.Background(), repo, cases, exec)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d want 1", len(results))
	}
	r := results[0]
	if r.Status != "defer" {
		t.Fatalf("status=%q want defer", r.Status)
	}
	if !strings.HasPrefix(r.Rationale, RationaleExecTimeout) {
		t.Fatalf("rationale=%q want prefix %q", r.Rationale, RationaleExecTimeout)
	}
	if strings.Contains(r.Rationale, "output mismatch") {
		t.Fatalf("rationale=%q must not be reported as an output diff", r.Rationale)
	}
}

type timeoutRegressExec struct {
	partial  string
	timeoutE error
}

func (m *timeoutRegressExec) ExecuteSQL(_ context.Context, _ string) (string, error) {
	return m.partial, m.timeoutE
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeRegressOutputStripsExecErrorSQLSTATEAndByteOffset(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{
			in:   "ERROR:  22001: value too long for type character varying(1) (byte 0)\n",
			want: "ERROR:  value too long for type character varying(1)",
		},
		{
			in:   "ERROR:  22P02: invalid input syntax for type oid: \"asdf\"\n",
			want: "ERROR:  invalid input syntax for type oid: \"asdf\"",
		},
	} {
		got := NormalizeRegressOutput(tc.in)
		if got != tc.want {
			t.Fatalf("NormalizeRegressOutput(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeRegressOutputDropsUnsupportedPLpgSQLExistsNoise(t *testing.T) {
	in := "ERROR:  EXISTS is not supported in PL/pgSQL expressions in v0\n" +
		"ERROR:  current transaction is aborted, commands ignored until end of transaction block\n" +
		"ROLLBACK;\n"
	got := NormalizeRegressOutput(in)
	want := "ROLLBACK;"
	if got != want {
		t.Fatalf("NormalizeRegressOutput unsupported PL/pgSQL noise = %q, want %q", got, want)
	}
}

func TestNormalizeRegressOutputDropsEmptyMVCCSizeBlock(t *testing.T) {
	in := " size_before | size_after\n" +
		"-------------+------------\n" +
		"(0 rows)\n" +
		"\n" +
		"ROLLBACK;\n"
	got := NormalizeRegressOutput(in)
	want := "ROLLBACK;"
	if got != want {
		t.Fatalf("NormalizeRegressOutput mvcc size block = %q, want %q", got, want)
	}
}
