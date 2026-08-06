package main

import (
	"strings"
	"testing"
)

// A realistic flag-OFF arm log: the OK lines carry digests, one query raised,
// and the log is interleaved with EXPLAIN bodies and runner chatter the parser
// must ignore.
const armOFF = `Q1: OK elapsed=12.34s rows=4 colsig=1111111111111111 ordered=aaaaaaaaaaaaaaaa unordered=bbbbbbbbbbbbbbbb
Q2: OK elapsed=3.10s rows=455 colsig=2222222222222222 ordered=cccccccccccccccc unordered=dddddddddddddddd
Q3: OK elapsed=8.00s rows=10 colsig=3333333333333333 ordered=eeeeeeeeeeeeeeee unordered=ffffffffffffffff
Q5: OK elapsed=5.00s rows=5 colsig=4444444444444444 ordered=0101010101010101 unordered=0202020202020202
Q7: OK elapsed=9.00s rows=4 colsig=5555555555555555 ordered=0303030303030303 unordered=0404040404040404
Q9: EXPLAIN plan:
  Hash Join  (cost=1.00..2.00 rows=1 width=8)
Q9: OK elapsed=85.46s rows=175 colsig=6666666666666666 ordered=0505050505050505 unordered=0606060606060606
Q17: OK elapsed=20.93s rows=1 colsig=7777777777777777 ordered=0707070707070707 unordered=0808080808080808
Q22: ERROR after 1.20s — pq: relation "x" does not exist
`

func TestParseRunLog(t *testing.T) {
	got, notes := parseRunLog(armOFF)
	if len(notes) != 0 {
		t.Errorf("unexpected notes: %v", notes)
	}
	if len(got) != 8 {
		t.Fatalf("parsed %d labels, want 8: %v", len(got), keysOf(got))
	}
	q2 := got["Q2"]
	if q2.status != "OK" || !q2.haveRows || q2.rows != 455 {
		t.Errorf("Q2 = %+v", q2)
	}
	if !q2.haveDigest() || q2.unordered != "dddddddddddddddd" || q2.colsig != "2222222222222222" {
		t.Errorf("Q2 digest = %+v", q2)
	}
	if q22 := got["Q22"]; q22.status != "ERROR" || !strings.Contains(q22.detail, "does not exist") {
		t.Errorf("Q22 = %+v", q22)
	}
	// The indented EXPLAIN body must not become a label.
	if _, bad := got["Hash"]; bad {
		t.Errorf("EXPLAIN body parsed as a result line")
	}
	if got["Q1"].seq >= got["Q22"].seq {
		t.Errorf("seq does not follow run order")
	}
}

func TestParseRunLogDuplicateLabelKeepsFirst(t *testing.T) {
	log := "Q1: OK elapsed=1.00s rows=4 colsig=11 ordered=aa unordered=bb\n" +
		"Q1: OK elapsed=2.00s rows=9 colsig=11 ordered=cc unordered=dd\n"
	got, notes := parseRunLog(log)
	if got["Q1"].rows != 4 {
		t.Errorf("kept the second occurrence: %+v", got["Q1"])
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "duplicate label Q1") {
		t.Errorf("notes = %v, want a duplicate-label note", notes)
	}
}

// TestDiffDetectsSilentCorruption is the acceptance-bar scenario: the ON arm
// returns the same row count for every query, and three of them are corrupt.
// Under the old row-count comparison this pair is indistinguishable from a
// clean run.
func TestDiffDetectsSilentCorruption(t *testing.T) {
	armON := strings.NewReplacer(
		"unordered=dddddddddddddddd", "unordered=d0d0d0d0d0d0d0d0", // Q2 values differ
		"unordered=0404040404040404", "unordered=0400000000000000", // Q7 values differ
		"ordered=eeeeeeeeeeeeeeee", "ordered=e0e0e0e0e0e0e0e0", // Q3 order only
	).Replace(armOFF)

	a, _ := parseRunLog(armOFF)
	b, _ := parseRunLog(armON)
	verdicts := diffDigests(a, b)
	byLabel := map[string]string{}
	for _, v := range verdicts {
		byLabel[v.label] = v.verdict
	}
	want := map[string]string{
		"Q2":  "VALUE-DIFF",
		"Q7":  "VALUE-DIFF",
		"Q3":  "ORDER-DIFF",
		"Q1":  "MATCH",
		"Q5":  "MATCH",
		"Q9":  "MATCH",
		"Q17": "MATCH",
		"Q22": "BOTH-ERROR",
	}
	for label, w := range want {
		if byLabel[label] != w {
			t.Errorf("%s: verdict %q, want %q", label, byLabel[label], w)
		}
	}
	if _, pass := renderDigestDiff("off.log", "on.log", verdicts, nil); pass {
		t.Errorf("diff passed despite two VALUE-DIFFs")
	}
}

func TestDiffVerdictClasses(t *testing.T) {
	base := "Q1: OK elapsed=1.00s rows=4 colsig=1111 ordered=aaaa unordered=bbbb\n"
	for _, tc := range []struct {
		name string
		b    string
		want string
	}{
		{"identical", base, "MATCH"},
		{"rows", "Q1: OK elapsed=1.00s rows=5 colsig=1111 ordered=aaaa unordered=bbbb\n", "ROWS-DIFF"},
		{"schema", "Q1: OK elapsed=1.00s rows=4 colsig=9999 ordered=aaaa unordered=bbbb\n", "SCHEMA-DIFF"},
		{"values", "Q1: OK elapsed=1.00s rows=4 colsig=1111 ordered=aaaa unordered=9999\n", "VALUE-DIFF"},
		{"order", "Q1: OK elapsed=1.00s rows=4 colsig=1111 ordered=9999 unordered=bbbb\n", "ORDER-DIFF"},
		{"status", "Q1: ERROR after 1.00s — pq: boom\n", "STATUS-DIFF"},
		{"nodigest", "Q1: OK elapsed=1.00s rows=4\n", "NO-DIGEST"},
		{"missing", "Q2: OK elapsed=1.00s rows=4 colsig=1111 ordered=aaaa unordered=bbbb\n", "MISSING-B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := parseRunLog(base)
			b, _ := parseRunLog(tc.b)
			verdicts := diffDigests(a, b)
			var got string
			for _, v := range verdicts {
				if v.label == "Q1" {
					got = v.verdict
				}
			}
			if got != tc.want {
				t.Fatalf("verdict %q, want %q", got, tc.want)
			}
			_, pass := renderDigestDiff("a", "b", verdicts, nil)
			if pass != (tc.want == "MATCH") {
				t.Errorf("pass = %v for verdict %s", pass, tc.want)
			}
		})
	}
}

// TestDiffNoDigestIsAFailure pins the decision that a run made WITHOUT -digest
// cannot read as a pass. Two arms that agree on every row count and carry no
// digests are exactly the state P5.9 run 1 was in.
func TestDiffNoDigestIsAFailure(t *testing.T) {
	plain := "Q1: OK elapsed=1.00s rows=4\nQ2: OK elapsed=2.00s rows=455\n"
	a, _ := parseRunLog(plain)
	b, _ := parseRunLog(plain)
	report, pass := renderDigestDiff("a", "b", diffDigests(a, b), nil)
	if pass {
		t.Errorf("a digest-less pair passed:\n%s", report)
	}
	if !strings.Contains(report, "NO-DIGEST") || !strings.Contains(report, "VERDICT: FAIL") {
		t.Errorf("report does not explain itself:\n%s", report)
	}
}

// TestRenderDigestDiffSelfPass is the shape of the P5.9-d bar itself: the
// flag-OFF arm diffed against a second flag-OFF arm. Every completed query must
// come back MATCH, or the instrument is too noisy to judge the ON arm with.
func TestRenderDigestDiffSelfPass(t *testing.T) {
	clean := strings.ReplaceAll(armOFF, `Q22: ERROR after 1.20s — pq: relation "x" does not exist`+"\n", "")
	a, _ := parseRunLog(clean)
	b, _ := parseRunLog(clean)
	report, pass := renderDigestDiff("off.log", "off2.log", diffDigests(a, b), nil)
	if !pass {
		t.Fatalf("an arm against itself must pass:\n%s", report)
	}
	if !strings.Contains(report, "7 MATCH") {
		t.Errorf("summary = %q", report)
	}
	if !strings.Contains(report, "VERDICT: PASS") {
		t.Errorf("missing verdict line:\n%s", report)
	}
}

// TestDiffBothErrorDoesNotPass: a query neither arm answered was not compared,
// so it cannot count as a match however identical the two failures are.
func TestDiffBothErrorDoesNotPass(t *testing.T) {
	same := "Q7: ERROR after 1.20s — pq: function does not exist\n"
	other := "Q7: ERROR after 1.20s — pq: relation does not exist\n"
	a, _ := parseRunLog(same)

	b, _ := parseRunLog(same)
	report, pass := renderDigestDiff("a", "b", diffDigests(a, b), nil)
	if pass || !strings.Contains(report, "BOTH-ERROR") {
		t.Errorf("identical failures passed the diff:\n%s", report)
	}

	c, _ := parseRunLog(other)
	report, pass = renderDigestDiff("a", "b", diffDigests(a, c), nil)
	if pass || !strings.Contains(report, "ERROR-DIFF") {
		t.Errorf("differing failures not distinguished:\n%s", report)
	}
}

func keysOf(m map[string]qResult) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
