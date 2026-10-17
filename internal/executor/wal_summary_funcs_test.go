package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/backup/walsummary"
)

// walSummaryRowText formats a row the way `psql -At` prints it.
func walSummaryRowText(r Row) string {
	parts := make([]string, len(r))
	for i, d := range r {
		switch d.Kind {
		case KindBool:
			parts[i] = map[bool]string{true: "t", false: "f"}[d.BoolValue()]
		case KindInt:
			parts[i] = strconv.FormatInt(d.Int, 10)
		default:
			parts[i] = d.StringValue()
		}
	}
	return strings.Join(parts, "|")
}

// TestWalSummaryFunctionsMatchPostgres pins pg_available_wal_summaries() and
// pg_wal_summary_contents() (walsummaryfuncs.c) against PG 18.3. The nine
// summary files in internal/backup/walsummary/testdata/pg18-summaries were
// written by a scratch PG 18.3; testdata/wal_summary_funcs.golden is that
// server's `psql -At` output for the four queries below over exactly those
// files (summarize_wal off while capturing, so it could not add any). The
// contents queries have no ORDER BY: PG returns each fork's limit-block row
// first, then the blocks in the file's stored order, and so must goopg. The
// last query drives the contents SRF laterally from the listing.
// pg_available_wal_summaries used to return no rows, and a reference to one
// of its columns failed with 42703. M0122-0009 S4a.
func TestWalSummaryFunctionsMatchPostgres(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	data := t.TempDir()
	dir := filepath.Join(data, walsummary.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join("..", "backup", "walsummary", "testdata", "pg18-summaries")
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx.DataDir = data
	var got strings.Builder
	for _, q := range []string{
		"SELECT * FROM pg_available_wal_summaries() ORDER BY start_lsn",
		"SELECT * FROM pg_wal_summary_contents(1, '0/174FA58', '0/175C540')",
		"SELECT * FROM pg_wal_summary_contents(1, '0/1000028', '0/10CAA50')",
		"SELECT s.start_lsn, count(*) FROM pg_available_wal_summaries() s, pg_wal_summary_contents(s.tli, s.start_lsn, s.end_lsn) c GROUP BY 1 ORDER BY 1",
	} {
		for _, r := range runQueryRows(t, ctx, q) {
			got.WriteString(walSummaryRowText(r) + "\n")
		}
	}
	want, err := os.ReadFile("testdata/wal_summary_funcs.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		gl, wl := strings.Split(got.String(), "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) && i < len(wl); i++ {
			if gl[i] != wl[i] {
				t.Fatalf("line %d: goopg %q, PG 18.3 %q (%d vs %d lines)", i+1, gl[i], wl[i], len(gl), len(wl))
			}
		}
		t.Fatalf("goopg printed %d lines, PG 18.3 %d", len(gl), len(wl))
	}

	// walsummaryfuncs.c's errors.
	for q, want := range map[string]struct{ code, msg string }{
		"SELECT * FROM pg_wal_summary_contents(0, '0/0', '0/0')": {"22023", "invalid timeline 0"},
		"SELECT * FROM pg_wal_summary_contents(1, '0/1', '0/2')": {"58P01",
			`could not open file "pg_wal/summaries/0000000100000000000000010000000000000002.summary": No such file or directory`},
	} {
		_, err := runQueryWithErr(ctx, q)
		var ee *ExecError
		if !errors.As(err, &ee) || ee.Code != want.code || ee.Message != want.msg {
			t.Errorf("%s: %v, want %s %q", q, err, want.code, want.msg)
		}
	}
	if rows := runQueryRows(t, ctx, "SELECT * FROM pg_wal_summary_contents(NULL, '0/1', '0/2')"); len(rows) != 0 {
		t.Errorf("a NULL argument returned %d rows; the function is strict", len(rows))
	}
}
