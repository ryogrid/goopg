package main

// Two-arm digest diff (M0127-P5.9-d).
//
// `tpch-runner -diff A.log B.log` reads two run logs produced with -digest and
// reports, per query label, whether the arms agree on VALUES and not merely on
// cardinality. It parses the runner's own stdout rather than a side-car file so
// an acceptance log stays the single artefact: the same file a human reads for
// timings is the file the diff consumes.
//
// The verdicts are deliberately unequal in strength:
//
//   - VALUE-DIFF is decisive. The multiset digests differ, so the arms returned
//     different tuples; row order and ORDER BY ties cannot explain it.
//   - ORDER-DIFF is a question, not a verdict. Multisets agree, scan order does
//     not. For a query whose ORDER BY is a total order that is a defect; for one
//     with ties (TPC-H Q3, Q10, Q18 …) two correct arms may break them
//     differently. The differ cannot tell which query is which — it reports the
//     distinction and leaves the classification to the operator, rather than
//     silently absolving it.
//   - NO-DIGEST is a failure, not a pass. It is how a run made WITHOUT -digest
//     is stopped from reading as "everything matched" — precisely the reading
//     that let P5.9 run 1's five silently-corrupt queries through.

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// qResult is one parsed per-query line from a run log.
type qResult struct {
	label     string
	status    string // "OK" or "ERROR"
	rows      int
	haveRows  bool
	colsig    string
	ordered   string
	unordered string
	detail    string // error text, for STATUS-DIFF reporting
	seq       int    // first-appearance order, so the report follows the run
}

func (q qResult) haveDigest() bool { return q.ordered != "" && q.unordered != "" }

// parseRunLog extracts the per-query result lines from a runner log. Lines it
// does not recognise (EXPLAIN bodies, CHECKPOINT, signal-file notices) are
// ignored. A label seen twice keeps its FIRST occurrence and produces a note —
// silently overwriting would hide a double-run of the same query.
func parseRunLog(text string) (map[string]qResult, []string) {
	out := map[string]qResult{}
	var notes []string
	seq := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		colon := strings.Index(line, ": ")
		if colon <= 0 {
			continue
		}
		label, rest := line[:colon], line[colon+2:]
		if strings.ContainsAny(label, " \t") {
			continue
		}
		var q qResult
		switch {
		case strings.HasPrefix(rest, "OK "), rest == "OK":
			q = qResult{label: label, status: "OK"}
		case strings.HasPrefix(rest, "ERROR "):
			q = qResult{label: label, status: "ERROR", detail: errDetail(rest)}
		default:
			continue // "EXPLAIN plan:", "signal-file detected", etc.
		}
		for _, tok := range strings.Fields(rest) {
			key, val, ok := strings.Cut(tok, "=")
			if !ok {
				continue
			}
			switch key {
			case "rows":
				if n, err := strconv.Atoi(val); err == nil {
					q.rows, q.haveRows = n, true
				}
			case "colsig":
				q.colsig = val
			case "ordered":
				q.ordered = val
			case "unordered":
				q.unordered = val
			}
		}
		if _, dup := out[label]; dup {
			notes = append(notes, fmt.Sprintf("duplicate label %s — keeping the first occurrence", label))
			continue
		}
		q.seq = seq
		seq++
		out[label] = q
	}
	return out, notes
}

// errDetail pulls the message after the em-dash the runner uses to separate an
// error's timing prefix from the driver's text.
func errDetail(rest string) string {
	if i := strings.Index(rest, "— "); i >= 0 {
		return rest[i+len("— "):]
	}
	return rest
}

// digestVerdict is one label's comparison outcome.
type digestVerdict struct {
	label   string
	verdict string
	detail  string
	seq     int
}

// ok reports whether a verdict lets the diff pass. Only MATCH does: every other
// outcome, ORDER-DIFF and NO-DIGEST included, needs a human to look.
func (v digestVerdict) ok() bool { return v.verdict == "MATCH" }

// diffDigests compares two parsed arms. Labels are reported in arm A's run
// order, with A-missing labels appended in B's order.
func diffDigests(a, b map[string]qResult) []digestVerdict {
	labels := make([]string, 0, len(a)+len(b))
	for l := range a {
		labels = append(labels, l)
	}
	for l := range b {
		if _, ok := a[l]; !ok {
			labels = append(labels, l)
		}
	}
	sort.Slice(labels, func(i, j int) bool {
		si, sj := sortKey(a, b, labels[i]), sortKey(a, b, labels[j])
		if si != sj {
			return si < sj
		}
		return labels[i] < labels[j]
	})

	out := make([]digestVerdict, 0, len(labels))
	for _, l := range labels {
		qa, inA := a[l]
		qb, inB := b[l]
		v := digestVerdict{label: l, seq: sortKey(a, b, l)}
		switch {
		case !inA:
			v.verdict, v.detail = "MISSING-A", "present only in arm B"
		case !inB:
			v.verdict, v.detail = "MISSING-B", "present only in arm A"
		case qa.status != qb.status:
			v.verdict = "STATUS-DIFF"
			v.detail = fmt.Sprintf("A=%s B=%s%s", qa.status, qb.status, firstErr(qa, qb))
		case qa.status == "ERROR" && qa.detail != qb.detail:
			v.verdict = "ERROR-DIFF"
			v.detail = fmt.Sprintf("A: %s | B: %s", qa.detail, qb.detail)
		case qa.status == "ERROR":
			// Both arms raised the same error. That is not a value match — it
			// is a query neither arm answered, so it cannot pass a diff whose
			// job is to certify values. Clause 1 of the bar fails it anyway.
			v.verdict = "BOTH-ERROR"
			v.detail = "both arms failed identically, nothing compared — " + qa.detail
		case qa.haveRows != qb.haveRows || qa.rows != qb.rows:
			v.verdict = "ROWS-DIFF"
			v.detail = fmt.Sprintf("A=%d B=%d", qa.rows, qb.rows)
		case !qa.haveDigest() || !qb.haveDigest():
			v.verdict = "NO-DIGEST"
			v.detail = "row counts agree but one or both arms ran without -digest — values were NOT compared"
		case qa.colsig != qb.colsig:
			v.verdict = "SCHEMA-DIFF"
			v.detail = fmt.Sprintf("column names/order differ (colsig A=%s B=%s)", qa.colsig, qb.colsig)
		case qa.unordered != qb.unordered:
			v.verdict = "VALUE-DIFF"
			v.detail = fmt.Sprintf("same %d rows, different tuples (unordered A=%s B=%s)", qa.rows, qa.unordered, qb.unordered)
		case qa.ordered != qb.ordered:
			v.verdict = "ORDER-DIFF"
			v.detail = "same multiset, different scan order — a defect unless this query's ORDER BY has ties"
		default:
			v.verdict = "MATCH"
			v.detail = fmt.Sprintf("rows=%d", qa.rows)
		}
		out = append(out, v)
	}
	return out
}

func sortKey(a, b map[string]qResult, label string) int {
	if q, ok := a[label]; ok {
		return q.seq
	}
	if q, ok := b[label]; ok {
		return 1_000_000 + q.seq
	}
	return 2_000_000
}

func firstErr(qa, qb qResult) string {
	if qa.status == "ERROR" && qa.detail != "" {
		return " — A: " + qa.detail
	}
	if qb.status == "ERROR" && qb.detail != "" {
		return " — B: " + qb.detail
	}
	return ""
}

// renderDigestDiff formats the report and reports whether the diff passed.
func renderDigestDiff(pathA, pathB string, verdicts []digestVerdict, notes []string) (string, bool) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "digest diff: A=%s B=%s\n", pathA, pathB)
	width := 0
	for _, v := range verdicts {
		if len(v.label) > width {
			width = len(v.label)
		}
	}
	counts := map[string]int{}
	pass := true
	for _, v := range verdicts {
		counts[v.verdict]++
		if !v.ok() {
			pass = false
		}
		fmt.Fprintf(&sb, "  %-*s  %-12s %s\n", width, v.label, v.verdict, v.detail)
	}
	for _, n := range notes {
		fmt.Fprintf(&sb, "  NOTE: %s\n", n)
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	fmt.Fprintf(&sb, "SUMMARY: %s\n", strings.Join(parts, ", "))
	if pass {
		sb.WriteString("VERDICT: PASS — every label matched on values, not merely on row count\n")
	} else {
		sb.WriteString("VERDICT: FAIL\n")
	}
	return sb.String(), pass
}

// runDigestDiff is the -diff entry point. Exits 1 on any non-MATCH so the mode
// is usable as a gate step, not only as a report.
func runDigestDiff(pathA, pathB string) {
	rawA, err := os.ReadFile(pathA)
	if err != nil {
		fail("-diff: %v", err)
	}
	rawB, err := os.ReadFile(pathB)
	if err != nil {
		fail("-diff: %v", err)
	}
	a, notesA := parseRunLog(string(rawA))
	b, notesB := parseRunLog(string(rawB))
	if len(a) == 0 {
		fail("-diff: no per-query result lines parsed from %s", pathA)
	}
	if len(b) == 0 {
		fail("-diff: no per-query result lines parsed from %s", pathB)
	}
	notes := append(append([]string(nil), prefixNotes("A", notesA)...), prefixNotes("B", notesB)...)
	report, pass := renderDigestDiff(pathA, pathB, diffDigests(a, b), notes)
	fmt.Print(report)
	if !pass {
		os.Exit(1)
	}
}

func prefixNotes(arm string, notes []string) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, arm+": "+n)
	}
	return out
}
