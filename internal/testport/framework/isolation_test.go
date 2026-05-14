package framework

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lib/pq"
)

type mockIsolationExec struct {
	calls []string
}

func (m *mockIsolationExec) ExecuteStep(_ context.Context, session string, sql string) (string, error) {
	m.calls = append(m.calls, session+":"+strings.TrimSpace(sql))
	if strings.Contains(sql, "defer-me") {
		return "", ErrDeferred
	}
	return "ok", nil
}

func TestParseAndRunIsolationPermutation(t *testing.T) {
	// Real spec format: steps declared under their session (position-based).
	spec := `
session "s1"
step "s1_begin" { BEGIN; }
step "s1_probe" {
  SELECT 1;
}

session "s2"
step "s2_begin" { BEGIN; }

permutation "s1_begin" "s2_begin" "s1_probe"
`
	path := filepath.Join(t.TempDir(), "demo.spec")
	if err := os.WriteFile(path, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseIsolationSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != 2 {
		t.Fatalf("sessions=%d want 2", len(parsed.Sessions))
	}
	if len(parsed.Permutations) != 1 {
		t.Fatalf("permutations=%d want 1", len(parsed.Permutations))
	}
	exec := &mockIsolationExec{}
	results, err := RunIsolationPermutation(context.Background(), parsed, 0, exec)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results=%d want 3", len(results))
	}
	for i := range results {
		if results[i].Status != "port" {
			t.Fatalf("step %d status=%q want port", i, results[i].Status)
		}
	}
	wantCalls := []string{"s1:BEGIN;", "s2:BEGIN;", "s1:SELECT 1;"}
	if !reflect.DeepEqual(exec.calls, wantCalls) {
		t.Fatalf("calls=%v want %v", exec.calls, wantCalls)
	}
}

func TestDiscoverIsolationSpecs(t *testing.T) {
	repo := t.TempDir()
	mustWrite := func(rel string) {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("session \"s1\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("postgres/src/test/isolation/specs/a.spec")
	mustWrite("postgres/src/test/isolation/specs/b.spec")
	paths, err := DiscoverIsolationSpecs(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths=%d want 2", len(paths))
	}
}

// TestNormalizeBoolWireText pins the lib/pq Go-bool → wire-text reversal that
// keeps IsolationRunner output byte-identical to upstream PostgreSQL's
// PQprint format for BOOL columns. M0100-0005.
func TestNormalizeBoolWireText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"true", "t"},
		{"false", "f"},
		{"t", "t"},   // already in wire form (defensive)
		{"f", "f"},   // already in wire form (defensive)
		{"", ""},     // NULL sentinel — caller already checked .Valid
		{"True", "True"}, // pq always lowercases Go bool — anything else is opaque text
	}
	for _, c := range cases {
		got := normalizeBoolWireText(c.in)
		if got != c.want {
			t.Errorf("normalizeBoolWireText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}


// TestParseIsolationSpecPreservesContinuationIndent pins M0100-0005b:
// upstream isolationtester echoes multi-line step SQL verbatim, with the
// first line concatenated to "step name: " and continuation lines
// preserving their leading whitespace.  Before M0100-0005b, readBlock
// applied TrimSpace to the line that contained the closing '}', which
// erased the continuation indentation that isolationtester (and the
// expected/<spec>.out file) rely on.  This regression locks in the
// fix: leading whitespace on the closing-brace line is kept; only the
// '}' itself and trailing whitespace are stripped.
func TestParseIsolationSpecPreservesContinuationIndent(t *testing.T) {
	spec := "session \"s1\"\n" +
		"step \"insert1\"    { INSERT INTO upsert VALUES (1, 11, 111)\n" +
		"                  ON CONFLICT (i) DO UPDATE SET k = EXCLUDED.k; }\n" +
		"permutation \"insert1\"\n"
	path := filepath.Join(t.TempDir(), "indent.spec")
	if err := os.WriteFile(path, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseIsolationSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := parsed.Steps["insert1"]
	if !ok {
		t.Fatalf("step insert1 not parsed; have %v", parsed.StepOrder)
	}
	want := "INSERT INTO upsert VALUES (1, 11, 111)\n" +
		"                  ON CONFLICT (i) DO UPDATE SET k = EXCLUDED.k;"
	if got.SQL != want {
		t.Fatalf("SQL mismatch:\n got: %q\nwant: %q", got.SQL, want)
	}
}

// TestParseIsolationSpecClosingBraceOnOwnLine ensures that when '}' is
// on its own line (no SQL content before it), the closing-brace line
// is not appended as a stray whitespace-only continuation line.  This
// is the common style in most upstream specs.
func TestParseIsolationSpecClosingBraceOnOwnLine(t *testing.T) {
	spec := "session \"s1\"\n" +
		"step \"q\" {\n" +
		"  SELECT 1;\n" +
		"  SELECT 2;\n" +
		"}\n" +
		"permutation \"q\"\n"
	path := filepath.Join(t.TempDir(), "ownline.spec")
	if err := os.WriteFile(path, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseIsolationSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := parsed.Steps["q"]
	if !ok {
		t.Fatalf("step q not parsed; have %v", parsed.StepOrder)
	}
	// When `{` is on its own line (brace-at-EOL layout), the SQL begins on
	// the next line, so the parsed SQL keeps a leading `\n`.  When `}` is
	// on its own line too, the SQL ends with a trailing `\n`.  This lets
	// the runner's `step %s: %s <waiting ...>\n` format render
	// `<waiting ...>` on its own line with a single leading space —
	// matching upstream isolationtester's verbatim echo for
	// `}`-on-own-line specs (e.g. merge-match-recheck expected output
	// where `\tUPDATE SET ...;\n <waiting ...>\n` appears).  The trailing
	// `}` line itself is dropped because nothing precedes the brace.
	want := "\n  SELECT 1;\n  SELECT 2;\n"
	if got.SQL != want {
		t.Fatalf("SQL mismatch:\n got: %q\nwant: %q", got.SQL, want)
	}
}


// TestFormatStepOutputMultiLineInlinesFirstLine pins upstream isolationtester's
// verbatim echo: when SQL is multi-line and the spec used the inline-brace
// layout (`step name { first\n  second; }`), the first SQL line sits on the
// `step <name>:` header line (no leading newline), and continuation lines
// follow with their indentation preserved.  Brace-at-EOL layout carries a
// leading newline in step.SQL, which renders as `step name: \n<body>`.
func TestFormatStepOutputMultiLineInlinesFirstLine(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "inline_brace_multi_line",
			sql:  "INSERT INTO upsert VALUES (1, 11, 111)\n                  ON CONFLICT (i) DO UPDATE SET k = EXCLUDED.k;",
			want: "step insert1: INSERT INTO upsert VALUES (1, 11, 111)\n                  ON CONFLICT (i) DO UPDATE SET k = EXCLUDED.k;\n",
		},
		{
			name: "brace_at_eol_multi_line",
			sql:  "\n    WITH t AS (\n        INSERT INTO colors VALUES(1, 'Red'))\n    SELECT * FROM colors;",
			want: "step insert1: \n    WITH t AS (\n        INSERT INTO colors VALUES(1, 'Red'))\n    SELECT * FROM colors;\n",
		},
		{
			name: "single_line",
			sql:  "SELECT 1;",
			want: "step q: SELECT 1;\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stepName := "insert1"
			if tc.name == "single_line" {
				stepName = "q"
			}
			got := formatStepOutput(stepName, tc.sql, stepOutcome{}, false)
			if got != tc.want {
				t.Fatalf("formatStepOutput mismatch:\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestFormatWaitingStepHeader pins the `<waiting ...>` suffix placement:
// upstream isolationtester appends ` <waiting ...>` to the SQL's final line
// (insert-conflict-do-update-4 expected output line 11), not on its own
// continuation line.  Multi-line and single-line SQL share the single format.
func TestFormatWaitingStepHeader(t *testing.T) {
	cases := []struct {
		name string
		step string
		sql  string
		want string
	}{
		{
			name: "single_line",
			step: "insert1",
			sql:  "INSERT INTO upsert VALUES (1);",
			want: "step insert1: INSERT INTO upsert VALUES (1); <waiting ...>\n",
		},
		{
			name: "inline_brace_multi_line",
			step: "insert1",
			sql:  "INSERT INTO upsert VALUES (1, 11, 111)\n                  ON CONFLICT (i) DO UPDATE SET k = EXCLUDED.k;",
			want: "step insert1: INSERT INTO upsert VALUES (1, 11, 111)\n                  ON CONFLICT (i) DO UPDATE SET k = EXCLUDED.k; <waiting ...>\n",
		},
		{
			name: "brace_at_eol_multi_line",
			step: "insert1",
			sql:  "\n    WITH t AS (\n        INSERT INTO colors VALUES(1, 'Red'))\n    SELECT * FROM colors;",
			want: "step insert1: \n    WITH t AS (\n        INSERT INTO colors VALUES(1, 'Red'))\n    SELECT * FROM colors; <waiting ...>\n",
		},
		{
			// merge-match-recheck shape: `{` at EOL AND `}` on its own
			// line.  The parser emits a SQL string that both starts and
			// ends with `\n`, so `<waiting ...>` appears on its own line
			// with the single leading space from the format string —
			// matching upstream isolationtester (e.g.
			// `expected/merge-match-recheck.out:14` shape
			// `\tUPDATE SET ...;\n <waiting ...>\n`).
			name: "brace_at_eol_close_own_line",
			step: "merge_status",
			sql:  "\n  MERGE INTO target t\n\tUPDATE SET status = 's4';\n",
			want: "step merge_status: \n  MERGE INTO target t\n\tUPDATE SET status = 's4';\n <waiting ...>\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatWaitingStepHeader(tc.step, tc.sql)
			if got != tc.want {
				t.Fatalf("formatWaitingStepHeader mismatch:\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestFormatPQErrorStripsSQLStateSuffix pins M0100-0005l: lib/pq's
// `(*pq.Error).Error()` returns `"pq: " + Message + " (" + Code + ")"`, but
// upstream PostgreSQL isolationtester prints only `PG_DIAG_MESSAGE_PRIMARY`
// (no trailing `(SQLSTATE)`). The runner must extract `Message` directly when
// it has a `*pq.Error` so spec output is byte-identical to upstream for every
// FK / unique-violation / partition-routing error path.
func TestFormatPQErrorStripsSQLStateSuffix(t *testing.T) {
	pqErr := &pq.Error{
		Code:    "23503",
		Message: `insert or update on table "fk_parted_pk_2" violates foreign key constraint "fk_parted_pk_a_fkey"`,
	}
	got := formatPQError(pqErr)
	want := `ERROR:  insert or update on table "fk_parted_pk_2" violates foreign key constraint "fk_parted_pk_a_fkey"`
	if got != want {
		t.Fatalf("formatPQError(*pq.Error) mismatch:\n got: %q\nwant: %q", got, want)
	}
	// Defensive: a wrapped *pq.Error must still yield the bare Message via
	// the type-assertion path. Direct equality is what callers see today —
	// this case documents the contract for future callers that wrap pq.Error.
	if errors.As(error(pqErr), new(*pq.Error)) != true {
		t.Fatalf("errors.As contract for *pq.Error broken — callers may wrap")
	}
}

// TestFormatPQErrorFallsBackOnNonPQ pins the non-pq path: arbitrary error
// values still get the legacy `"pq: "` prefix trim. Used by runner code paths
// that surface harness-internal errors (Scan failures, context cancellation,
// connection-pool errors) where there is no SQLSTATE to strip.
func TestFormatPQErrorFallsBackOnNonPQ(t *testing.T) {
	cases := []struct{ in error; want string }{
		{errors.New("pq: connection closed"), "ERROR:  connection closed"},
		{errors.New("driver: bad connection"), "ERROR:  driver: bad connection"},
	}
	for _, c := range cases {
		got := formatPQError(c.in)
		if got != c.want {
			t.Errorf("formatPQError(%q) = %q, want %q", c.in.Error(), got, c.want)
		}
	}
	if got := formatPQError(nil); got != "" {
		t.Errorf("formatPQError(nil) = %q, want empty", got)
	}
}
