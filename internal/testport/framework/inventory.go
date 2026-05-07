package framework

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SuiteInventory is one canonical migration-target row for M0060.
type SuiteInventory struct {
	SuiteID         string
	UpstreamPattern string
	Kind            string
	Count           int
	Example         string
}

// BuildSuiteInventory scans upstream PostgreSQL test trees and returns
// deterministic counts for the M0060 migration target families.
func BuildSuiteInventory(repoRoot string) ([]SuiteInventory, error) {
	type row struct {
		suiteID string
		pattern string
		kind    string
		count   int
		example string
	}

	regressSQL, regressExample, err := countByGlob(repoRoot, "postgres/src/test/regress/sql/*.sql")
	if err != nil {
		return nil, err
	}
	regressExpected, _, err := countByGlob(repoRoot, "postgres/src/test/regress/expected/*.out")
	if err != nil {
		return nil, err
	}
	isolationSpecs, isolationExample, err := countByGlob(repoRoot, "postgres/src/test/isolation/specs/*.spec")
	if err != nil {
		return nil, err
	}
	recoveryTAP, recoveryExample, err := countByGlob(repoRoot, "postgres/src/test/recovery/t/*.pl")
	if err != nil {
		return nil, err
	}
	subscriptionTAP, subscriptionExample, err := countByGlob(repoRoot, "postgres/src/test/subscription/t/*.pl")
	if err != nil {
		return nil, err
	}
	clientToolTAP, clientToolExample, err := countByGlob(repoRoot, "postgres/src/bin/*/t/*.pl")
	if err != nil {
		return nil, err
	}
	moduleSuites, moduleExample, err := countFeatureDirs(repoRoot, filepath.Join(repoRoot, "postgres/src/test/modules"))
	if err != nil {
		return nil, err
	}
	contribSuites, contribExample, err := countFeatureDirs(repoRoot, filepath.Join(repoRoot, "postgres/contrib"))
	if err != nil {
		return nil, err
	}

	rows := []row{
		{
			suiteID: "regress-sql",
			pattern: "postgres/src/test/regress/sql/*.sql",
			kind:    "regress",
			count:   regressSQL,
			example: regressExample,
		},
		{
			suiteID: "regress-expected",
			pattern: "postgres/src/test/regress/expected/*.out",
			kind:    "regress",
			count:   regressExpected,
			example: "postgres/src/test/regress/expected/create_table.out",
		},
		{
			suiteID: "isolation-specs",
			pattern: "postgres/src/test/isolation/specs/*.spec",
			kind:    "isolation",
			count:   isolationSpecs,
			example: isolationExample,
		},
		{
			suiteID: "recovery-tap",
			pattern: "postgres/src/test/recovery/t/*.pl",
			kind:    "tap",
			count:   recoveryTAP,
			example: recoveryExample,
		},
		{
			suiteID: "subscription-tap",
			pattern: "postgres/src/test/subscription/t/*.pl",
			kind:    "tap",
			count:   subscriptionTAP,
			example: subscriptionExample,
		},
		{
			suiteID: "client-tools-tap",
			pattern: "postgres/src/bin/*/t/*.pl",
			kind:    "tap",
			count:   clientToolTAP,
			example: clientToolExample,
		},
		{
			suiteID: "modules-suites",
			pattern: "postgres/src/test/modules/*",
			kind:    "mixed",
			count:   moduleSuites,
			example: moduleExample,
		},
		{
			suiteID: "contrib-suites",
			pattern: "postgres/contrib/*",
			kind:    "mixed",
			count:   contribSuites,
			example: contribExample,
		},
	}

	out := make([]SuiteInventory, 0, len(rows))
	for _, r := range rows {
		out = append(out, SuiteInventory{
			SuiteID:         r.suiteID,
			UpstreamPattern: r.pattern,
			Kind:            r.kind,
			Count:           r.count,
			Example:         r.example,
		})
	}
	return out, nil
}

func countByGlob(repoRoot, pattern string) (count int, example string, err error) {
	glob := filepath.Join(repoRoot, filepath.FromSlash(pattern))
	matches, err := filepath.Glob(glob)
	if err != nil {
		return 0, "", fmt.Errorf("glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return 0, "", nil
	}
	sort.Strings(matches)
	rel, err := filepath.Rel(repoRoot, matches[0])
	if err != nil {
		return 0, "", fmt.Errorf("rel path %q: %w", matches[0], err)
	}
	return len(matches), filepath.ToSlash(rel), nil
}

func countFeatureDirs(repoRoot, root string) (count int, example string, err error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "", nil
		}
		return 0, "", fmt.Errorf("read dir %q: %w", root, err)
	}
	suites := make([]string, 0)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		has, err := hasFeatureFiles(dir)
		if err != nil {
			return 0, "", err
		}
		if has {
			suites = append(suites, dir)
		}
	}
	if len(suites) == 0 {
		return 0, "", nil
	}
	sort.Strings(suites)
	count = len(suites)
	rel, err := filepath.Rel(repoRoot, suites[0])
	if err != nil {
		// Fallback for unusual roots; return path from the "postgres" segment.
		example = pathFromPostgres(suites[0])
		return count, example, nil
	}
	example = filepath.ToSlash(rel)
	return count, example, nil
}

func hasFeatureFiles(dir string) (bool, error) {
	markers := []string{
		"sql",
		"expected",
		"t",
	}
	for _, marker := range markers {
		entries, err := os.ReadDir(filepath.Join(dir, marker))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, fmt.Errorf("read dir %q: %w", filepath.Join(dir, marker), err)
		}
		if len(entries) > 0 {
			return true, nil
		}
	}
	// Some suites keep top-level .sql/.pl files directly under the suite dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("read dir %q: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".sql" || ext == ".pl" || ext == ".spec" {
			return true, nil
		}
	}
	return false, nil
}

func pathFromPostgres(abs string) string {
	norm := filepath.ToSlash(abs)
	idx := strings.Index(norm, "postgres/")
	if idx >= 0 {
		return norm[idx:]
	}
	return norm
}
