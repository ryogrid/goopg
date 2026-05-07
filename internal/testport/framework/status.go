package framework

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// StatusRow is a machine-readable row in postgres-oracle-port-status.csv.
type StatusRow struct {
	ID           string
	UpstreamPath string
	SuiteType    string
	Status       string
	PassRequired string
	Rationale    string
	DeferredTo   string
}

// StatusCounts summarizes status totals for a suite type.
type StatusCounts struct {
	Port     int
	Defer    int
	Excluded int
}

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
	required := []string{"id", "upstream_path", "suite_type", "status", "pass_required", "rationale", "deferred_to"}
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
			UpstreamPath: val("upstream_path"),
			SuiteType:    val("suite_type"),
			Status:       strings.ToLower(val("status")),
			PassRequired: strings.ToLower(val("pass_required")),
			Rationale:    val("rationale"),
			DeferredTo:   val("deferred_to"),
		}
		if row.ID == "" {
			return nil, fmt.Errorf("csv %q row %d: id is empty", path, rowNum)
		}
		out = append(out, row)
	}
	return out, nil
}

func ValidateStatusRows(rows []StatusRow) error {
	if len(rows) == 0 {
		return fmt.Errorf("status rows are empty")
	}
	seenID := map[string]struct{}{}
	for i, r := range rows {
		rowNum := i + 1
		if _, ok := seenID[r.ID]; ok {
			return fmt.Errorf("duplicate id %q", r.ID)
		}
		seenID[r.ID] = struct{}{}

		if r.UpstreamPath == "" || !strings.HasPrefix(r.UpstreamPath, "postgres/") {
			return fmt.Errorf("row %d (%s): invalid upstream_path %q", rowNum, r.ID, r.UpstreamPath)
		}
		switch r.Status {
		case "port", "defer", "excluded":
		default:
			return fmt.Errorf("row %d (%s): unsupported status %q", rowNum, r.ID, r.Status)
		}
		if r.PassRequired != "yes" && r.PassRequired != "no" {
			return fmt.Errorf("row %d (%s): pass_required must be yes/no", rowNum, r.ID)
		}
		if r.Rationale == "" {
			return fmt.Errorf("row %d (%s): rationale is required", rowNum, r.ID)
		}
		if r.Status == "defer" && r.DeferredTo == "" {
			return fmt.Errorf("row %d (%s): defer requires deferred_to", rowNum, r.ID)
		}
		if r.Status == "excluded" && r.DeferredTo != "" && r.DeferredTo != "-" {
			return fmt.Errorf("row %d (%s): excluded must use deferred_to '-' or empty", rowNum, r.ID)
		}
	}
	return nil
}

func SummarizeBySuite(rows []StatusRow) map[string]StatusCounts {
	out := map[string]StatusCounts{}
	for _, r := range rows {
		c := out[r.SuiteType]
		switch r.Status {
		case "port":
			c.Port++
		case "defer":
			c.Defer++
		case "excluded":
			c.Excluded++
		}
		out[r.SuiteType] = c
	}
	return out
}

func SuiteKeys(summary map[string]StatusCounts) []string {
	keys := make([]string, 0, len(summary))
	for k := range summary {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func WriteStatusMarkdown(w io.Writer, rows []StatusRow) error {
	if err := ValidateStatusRows(rows); err != nil {
		return err
	}
	summary := SummarizeBySuite(rows)
	keys := SuiteKeys(summary)

	_, _ = fmt.Fprintln(w, "# PostgreSQL Oracle Test-Port Status")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Generated from `docs/test-port/postgres-oracle-port-status.csv`.")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Status meanings:")
	_, _ = fmt.Fprintln(w, "- `port`: migrated and pass-required.")
	_, _ = fmt.Fprintln(w, "- `defer`: in scope, not yet pass-required.")
	_, _ = fmt.Fprintln(w, "- `excluded`: explicitly out of scope by policy.")
	_, _ = fmt.Fprintln(w)

	_, _ = fmt.Fprintln(w, "## Suite Summary")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "| suite_type | port | defer | excluded |")
	_, _ = fmt.Fprintln(w, "| ---------- | ----:| -----:| --------:|")
	for _, k := range keys {
		c := summary[k]
		_, _ = fmt.Fprintf(w, "| %s | %d | %d | %d |\n", k, c.Port, c.Defer, c.Excluded)
	}
	_, _ = fmt.Fprintln(w)

	_, _ = fmt.Fprintln(w, "## Entries")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "| id | upstream_path | suite_type | status | pass_required | rationale | deferred_to |")
	_, _ = fmt.Fprintln(w, "|----|---------------|------------|--------|---------------|-----------|-------------|")
	for _, r := range rows {
		deferredTo := r.DeferredTo
		if deferredTo == "" {
			deferredTo = "-"
		}
		_, _ = fmt.Fprintf(w, "| %s | `%s` | %s | %s | %s | %s | `%s` |\n",
			r.ID,
			r.UpstreamPath,
			r.SuiteType,
			r.Status,
			r.PassRequired,
			r.Rationale,
			deferredTo,
		)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "## Notes")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "- Every non-passing target must be present here as `defer` or `excluded`.")
	_, _ = fmt.Fprintln(w, "- `defer` entries must reference a follow-up milestone task.")
	return nil
}
