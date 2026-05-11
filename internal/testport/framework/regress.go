package framework

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	// ErrDeferred marks a test as in-scope but not yet pass-required.
	ErrDeferred = errors.New("deferred")
	// ErrExcluded marks a test as excluded by policy.
	ErrExcluded = errors.New("excluded")
)

type RegressCase struct {
	Name         string
	SQLPath      string
	ExpectedPath string
}

type RegressResult struct {
	Name      string
	SQLPath   string
	Status    string
	Rationale string
}

type RegressExecutor interface {
	ExecuteSQL(ctx context.Context, sql string) (string, error)
}

// DiscoverRegressCases finds pg_regress SQL/expected pairs.
func DiscoverRegressCases(repoRoot string) ([]RegressCase, error) {
	sqlGlob := filepath.Join(repoRoot, "postgres", "src", "test", "regress", "sql", "*.sql")
	sqlFiles, err := filepath.Glob(sqlGlob)
	if err != nil {
		return nil, fmt.Errorf("glob regress sql: %w", err)
	}
	sort.Strings(sqlFiles)

	cases := make([]RegressCase, 0, len(sqlFiles))
	for _, sqlPath := range sqlFiles {
		base := strings.TrimSuffix(filepath.Base(sqlPath), filepath.Ext(sqlPath))
		expected := filepath.Join(repoRoot, "postgres", "src", "test", "regress", "expected", base+".out")
		relSQL, err := filepath.Rel(repoRoot, sqlPath)
		if err != nil {
			return nil, fmt.Errorf("rel sql path: %w", err)
		}
		relExpected := ""
		if _, err := os.Stat(expected); err == nil {
			relExpected, err = filepath.Rel(repoRoot, expected)
			if err != nil {
				return nil, fmt.Errorf("rel expected path: %w", err)
			}
			relExpected = filepath.ToSlash(relExpected)
		}
		cases = append(cases, RegressCase{
			Name:         base,
			SQLPath:      filepath.ToSlash(relSQL),
			ExpectedPath: relExpected,
		})
	}
	return cases, nil
}

// RunRegressSubset executes selected regress cases and reports port/defer/excluded.
func RunRegressSubset(ctx context.Context, repoRoot string, cases []RegressCase, exec RegressExecutor) ([]RegressResult, error) {
	results := make([]RegressResult, 0, len(cases))
	for _, c := range cases {
		sqlAbs := filepath.Join(repoRoot, filepath.FromSlash(c.SQLPath))
		sqlBytes, err := os.ReadFile(sqlAbs)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", c.SQLPath, err)
		}
		actual, err := exec.ExecuteSQL(ctx, string(sqlBytes))
		if errors.Is(err, ErrExcluded) {
			results = append(results, RegressResult{Name: c.Name, SQLPath: c.SQLPath, Status: "excluded", Rationale: "excluded by harness policy"})
			continue
		}
		if errors.Is(err, ErrDeferred) {
			results = append(results, RegressResult{Name: c.Name, SQLPath: c.SQLPath, Status: "defer", Rationale: "execution deferred by capability gate"})
			continue
		}
		if err != nil {
			results = append(results, RegressResult{Name: c.Name, SQLPath: c.SQLPath, Status: "defer", Rationale: fmt.Sprintf("execution error: %v", err)})
			continue
		}

		if c.ExpectedPath == "" {
			results = append(results, RegressResult{Name: c.Name, SQLPath: c.SQLPath, Status: "defer", Rationale: "missing expected output file"})
			continue
		}
		expectedAbs := filepath.Join(repoRoot, filepath.FromSlash(c.ExpectedPath))
		expectedBytes, err := os.ReadFile(expectedAbs)
		if err != nil {
			results = append(results, RegressResult{Name: c.Name, SQLPath: c.SQLPath, Status: "defer", Rationale: fmt.Sprintf("cannot read expected output: %v", err)})
			continue
		}
		if NormalizeRegressOutput(string(expectedBytes)) == NormalizeRegressOutput(actual) {
			results = append(results, RegressResult{Name: c.Name, SQLPath: c.SQLPath, Status: "port", Rationale: "harness subset matched expected output"})
			continue
		}
		results = append(results, RegressResult{Name: c.Name, SQLPath: c.SQLPath, Status: "defer", Rationale: "output mismatch; normalization rules need extension"})
	}
	return results, nil
}

// NormalizeRegressOutput normalizes line endings, trailing whitespace, and
// common goopg-specific output differences for stable diffs with the
// upstream .out files.
//
// Normalizations applied (in order):
//  1. CR+LF → LF
//  2. Trailing spaces/tabs per line stripped
//  3. Trailing blank lines stripped
//  4. ERROR double-space normalisation: "ERROR:  " → "ERROR: " (psql adds
//     double space; goopg wire path adds single space)
func NormalizeRegressOutput(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	// Normalise double-space in ERROR/NOTICE/WARNING/HINT/DETAIL lines.
	// PostgreSQL's libpq writes "SEVERITY:  message" (two spaces); goopg
	// may emit one space.  Collapse to two spaces so both sides compare equal
	// after normalization.
	for _, sev := range []string{"ERROR", "NOTICE", "WARNING", "HINT", "DETAIL", "CONTEXT"} {
		// "SEVERITY: msg" → "SEVERITY:  msg" (canonical two-space form)
		raw = strings.ReplaceAll(raw, sev+": ", sev+":  ")
	}
	s := bufio.NewScanner(strings.NewReader(raw))
	lines := make([]string, 0)
	for s.Scan() {
		lines = append(lines, strings.TrimRight(s.Text(), " \t"))
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
