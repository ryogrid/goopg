package framework

import (
	"encoding/csv"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// StatusRow is a machine-readable row in the single authority inventory CSV,
// `docs/test-port/postgres-oracle-target-inventory.csv`. It is the consolidated
// successor to the former `postgres-oracle-port-status.csv` (governance) and
// `regress-diff-baseline.csv` (regress baseline), merged per
// `tmp/porting-info-improvement-plan.md`.
type StatusRow struct {
	ID           string
	SuiteID      string
	Kind         string
	ItemPath     string
	Status       string
	PassRequired string
	DeferredTo   string
	Rationale    string
}

// validStatus is the unified status vocabulary. `pass`/`failed`/`not-tried`
// describe per-case execution outcome; `port`/`defer`/`excluded` describe the
// governance decision. must-pass is expressed by pass_required == "yes".
var validStatus = map[string]bool{
	"pass":      true,
	"failed":    true,
	"not-tried": true,
	"excluded":  true,
	"port":      true,
	"defer":     true,
}

// testFuncRe matches a Go test function name. `port` rows (the TAP must-pass
// set that ci/batch/lib/summarize.py presence-checks) must name one in their
// rationale, so this is also the presence-warning extraction contract.
var testFuncRe = regexp.MustCompile(`Test(?:Port|E2E)_\w+`)

// LoadStatusCSV parses the inventory CSV, resolving columns by header name so
// extra columns are ignored and column order is irrelevant.
func LoadStatusCSV(path string) ([]StatusRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read csv %q: %w", path, err)
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("csv %q is empty", path)
	}

	head := recs[0]
	idx := map[string]int{}
	for i, h := range head {
		idx[strings.TrimSpace(strings.ToLower(h))] = i
	}
	required := []string{"id", "suite_id", "kind", "item_path", "status", "pass_required", "deferred_to", "rationale"}
	for _, k := range required {
		if _, ok := idx[k]; !ok {
			return nil, fmt.Errorf("csv %q missing required column %q", path, k)
		}
	}

	out := make([]StatusRow, 0, len(recs)-1)
	for i, rec := range recs[1:] {
		if len(rec) == 0 {
			continue
		}
		rowNum := i + 2
		val := func(col string) string {
			j := idx[col]
			if j >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[j])
		}
		row := StatusRow{
			ID:           val("id"),
			SuiteID:      val("suite_id"),
			Kind:         val("kind"),
			ItemPath:     val("item_path"),
			Status:       strings.ToLower(val("status")),
			PassRequired: strings.ToLower(val("pass_required")),
			DeferredTo:   val("deferred_to"),
			Rationale:    val("rationale"),
		}
		if row.ItemPath == "" {
			return nil, fmt.Errorf("csv %q row %d: item_path is empty", path, rowNum)
		}
		out = append(out, row)
	}
	return out, nil
}

// ValidateStatusRows checks the on-disk inventory CSV for the breakage classes
// that historically broke the toolchain: malformed status/pass_required
// vocabulary, an `excluded` row marked must-pass, a `defer` row with no
// deferred_to, a `port` row whose rationale does not name its pinning test
// func (the presence-warning contract), and duplicate non-empty ids.
func ValidateStatusRows(rows []StatusRow) error {
	if len(rows) == 0 {
		return fmt.Errorf("status rows are empty")
	}
	seenID := map[string]struct{}{}
	for i, r := range rows {
		rowNum := i + 1
		label := r.ID
		if label == "" {
			label = r.ItemPath
		}
		if r.ID != "" {
			if _, ok := seenID[r.ID]; ok {
				return fmt.Errorf("duplicate id %q", r.ID)
			}
			seenID[r.ID] = struct{}{}
		}
		if !strings.HasPrefix(r.ItemPath, "postgres/") {
			return fmt.Errorf("row %d (%s): invalid item_path %q", rowNum, label, r.ItemPath)
		}
		if !validStatus[r.Status] {
			return fmt.Errorf("row %d (%s): unsupported status %q", rowNum, label, r.Status)
		}
		if r.PassRequired != "yes" && r.PassRequired != "no" {
			return fmt.Errorf("row %d (%s): pass_required must be yes/no", rowNum, label)
		}
		if r.Status == "excluded" && r.PassRequired == "yes" {
			return fmt.Errorf("row %d (%s): excluded cannot be pass_required=yes", rowNum, label)
		}
		if r.Status == "defer" && r.DeferredTo == "" {
			return fmt.Errorf("row %d (%s): defer requires deferred_to", rowNum, label)
		}
		if r.Status == "port" && !testFuncRe.MatchString(r.Rationale) {
			return fmt.Errorf("row %d (%s): port rationale must name a TestPort_*/TestE2E_* func", rowNum, label)
		}
	}
	return nil
}
