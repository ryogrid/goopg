package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseCSV = `id,suite_id,kind,item_path,status,pass_required,deferred_to,rationale
R-a,regress-sql,regress,postgres/src/test/regress/sql/alpha.sql,failed,no,,"quoted, comma"
R-b,regress-sql,regress,postgres/src/test/regress/sql/beta.sql,pass,yes,,ok
R-c,regress-sql,regress,postgres/src/test/regress/sql/gamma.sql,failed,no,,x
I-a,isolation,isolation,postgres/src/test/isolation/specs/alpha.spec,failed,no,,iso
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "inv.csv")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustParse(t *testing.T, tsv string) []count {
	t.Helper()
	c, err := parseCounts(strings.NewReader(tsv))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// With the columns already present and no counts, the rewrite is byte-identical.
func TestRoundTripIdentityNoCounts(t *testing.T) {
	body := strings.Replace(baseCSV, "rationale\n", "rationale,mismatch_lines,expected_lines\n", 1)
	body = strings.ReplaceAll(body, "\"quoted, comma\"\n", "\"quoted, comma\",3,30\n")
	body = strings.ReplaceAll(body, ",ok\n", ",ok,,\n")
	body = strings.ReplaceAll(body, ",x\n", ",x,,\n")
	body = strings.ReplaceAll(body, ",iso\n", ",iso,,\n")
	p := writeTemp(t, body)
	var out bytes.Buffer
	if err := update(p, mustParse(t, "# nothing\n\n"), &out); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != body {
		t.Fatalf("round trip changed bytes:\n%s\nwant:\n%s", got, body)
	}
}

func TestAddsColumns(t *testing.T) {
	p := writeTemp(t, baseCSV)
	var out bytes.Buffer
	if err := update(p, nil, &out); err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(read(t, p), "\n"), "\n")
	want := strings.Split(strings.TrimSuffix(baseCSV, "\n"), "\n")
	if got[0] != want[0]+",mismatch_lines,expected_lines" {
		t.Fatalf("header = %q", got[0])
	}
	for i := 1; i < len(want); i++ {
		if got[i] != want[i]+",," {
			t.Fatalf("row %d = %q, want %q", i, got[i], want[i]+",,")
		}
	}
}

func TestUpdateFailedRowsOnly(t *testing.T) {
	p := writeTemp(t, baseCSV)
	var out bytes.Buffer
	tsv := "alpha\tFAIL\t5\t100\nbeta\tFAIL\t2\t50\ngamma\tPASS\t0\t40\nnosuch\tFAIL\t1\t2\n"
	if err := update(p, mustParse(t, tsv), &out); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	for _, want := range []string{
		`"quoted, comma",5,100` + "\n",
		",pass,yes,,ok,,\n",
		",failed,no,,x,0,40\n",
		",failed,no,,iso,,\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("csv missing %q:\n%s", want, got)
		}
	}
	rep := out.String()
	for _, want := range []string{"alpha: -→5/100", "pass-status row beta measured 2/50", "gamma: pass-flip candidate", "unknown case nosuch"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report missing %q:\n%s", want, rep)
		}
	}
}

func TestTimeoutKeepsOldValue(t *testing.T) {
	p := writeTemp(t, baseCSV)
	var out bytes.Buffer
	if err := update(p, mustParse(t, "alpha\tFAIL\t5\t100\n"), &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := update(p, mustParse(t, "alpha\tTIMEOUT\t\t\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, p), `"quoted, comma",5,100`) {
		t.Fatalf("TIMEOUT overwrote old value:\n%s", read(t, p))
	}
	if !strings.Contains(out.String(), "alpha: TIMEOUT, kept 5/100") {
		t.Fatalf("report = %q", out.String())
	}
}

func TestParseCountsMalformed(t *testing.T) {
	for _, tsv := range []string{
		"alpha\tFAIL\t5\n",
		"alpha\tWHAT\t1\t2\n",
		"alpha\tFAIL\tx\t2\n",
		"alpha\tFAIL\t3\t2\n",
		"alpha\tFAIL\t-1\t2\n",
	} {
		if _, err := parseCounts(strings.NewReader(tsv)); err == nil {
			t.Errorf("parseCounts(%q): expected error", tsv)
		}
	}
}
