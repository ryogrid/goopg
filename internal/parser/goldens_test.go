package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

)

// The golden corpus is the CUTOVER's oracle. Every assertParity / assertBothReject
// call in this package compares the yacc parser against the LEGACY parser, which
// is exactly the right check while both exist — and is worth nothing the moment
// the legacy statement parsers are deleted (P7.2), because the comparison
// silently becomes a tautology.
//
// So each of those calls also records what the yacc parser produced, keyed by
// the statement text, and TestParityGoldensAreCurrent re-verifies the whole
// recorded set against a fresh parse. After the cutover the legacy half of
// assertParity goes away and these goldens become the sole pin — with no gap in
// coverage, because they were captured while the legacy oracle still agreed.
//
// Regenerate with GOOPG_UPDATE_GOLDENS=1 go test ./internal/sqlparser/ .
// A regeneration diff is a REVIEW ITEM: it means a pinned AST changed.
const goldenPath = "testdata/parity_goldens.txt"

var (
	goldenMu      sync.Mutex
	goldenSeen    = map[string]string{}
	goldenRecords = map[string]string{}
)

// recordGolden captures one statement's yacc-side result. A rejected statement
// records the "!" marker plus its message, so assertBothReject's direction is
// pinned too.
func recordGolden(q, dump string) {
	goldenMu.Lock()
	defer goldenMu.Unlock()
	goldenSeen[q] = dump
}

func loadGoldens(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(goldenPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}
		}
		t.Fatalf("read goldens: %v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		q, dump, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		out[unescapeGolden(q)] = unescapeGolden(dump)
	}
	return out
}

// escapeGolden keeps one record on one line: the file is line-oriented so a
// statement with an embedded newline (a dollar-quoted body, a wrapped CREATE
// TABLE) cannot be stored verbatim.
func escapeGolden(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}

func unescapeGolden(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// yaccDump is the recorded form: the canonical AST dump, or "!<message>" when
// the parser rejects the statement.
func yaccDump(q string) string {
	toks, err := Lex(q)
	if err != nil {
		return "!lex: " + err.Error()
	}
	stmts, err := ParseOneSrc(q, toks)
	if err != nil {
		return "!" + err.Error()
	}
	return dumpStmts(stmts)
}

// TestParityGoldensAreCurrent re-parses every recorded statement and compares
// it against the file. It runs LAST (Go runs tests in source order within a
// package, and this file's name puts it after the recorders alphabetically only
// by luck) — so it re-derives the dumps itself rather than trusting the map the
// other tests filled, which makes it correct under -run filters too.
func TestParityGoldensAreCurrent(t *testing.T) {
	want := loadGoldens(t)
	if os.Getenv("GOOPG_UPDATE_GOLDENS") == "1" {
		t.Skip("regeneration runs in TestParityGoldensRegenerate")
	}
	if len(want) == 0 {
		t.Skip("no goldens recorded yet")
	}
	stale := 0
	for q, expect := range want {
		got := yaccDump(q)
		if got != expect {
			stale++
			if stale <= 10 {
				t.Errorf("golden drift %q\n want=%s\n got =%s", q, truncForLog(expect), truncForLog(got))
			}
		}
	}
	if stale > 10 {
		t.Errorf("... and %d more golden drifts", stale-10)
	}
}

// TestMain writes the goldens AFTER every test in the package has run.
//
// The regenerator used to be a test named TestZZZParityGoldensRegenerate, on
// the assumption that the ZZZ prefix made it last. It does not: `go test`
// runs tests in SOURCE ORDER — file by file, alphabetically — so a regenerator
// living in goldens_test.go ran before every file after "g" in the alphabet,
// and silently recorded nothing for them. That is how a cutover oracle ends up
// missing the statements it exists to pin. TestMain is the only hook that is
// genuinely last.
func TestMain(m *testing.M) {
	code := m.Run()
	if os.Getenv("GOOPG_UPDATE_GOLDENS") == "1" {
		if err := writeGoldens(); err != nil {
			fmt.Fprintln(os.Stderr, "golden regeneration failed:", err)
			code = 1
		}
	}
	os.Exit(code)
}

func writeGoldens() error {
	goldenMu.Lock()
	defer goldenMu.Unlock()
	keys := make([]string, 0, len(goldenSeen))
	for q := range goldenSeen {
		keys = append(keys, q)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Parity goldens — the cutover's oracle; see goldens_test.go.\n")
	b.WriteString("# Regenerate: GOOPG_UPDATE_GOLDENS=1 go test ./internal/parser/\n")
	b.WriteString(fmt.Sprintf("# %d statements\n", len(keys)))
	for _, q := range keys {
		b.WriteString(escapeGolden(q) + "\t" + escapeGolden(goldenSeen[q]) + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(goldenPath, []byte(b.String()), 0o644)
}

var _ = goldenRecords

// goldenFor returns the recorded dump for one statement. The file is loaded
// once per run; a miss means the statement is new and must be reviewed into
// the corpus rather than silently passing.
var (
	goldenLoadOnce sync.Once
	goldenLoaded   map[string]string
)

func goldenFor(t *testing.T, q string) (string, bool) {
	t.Helper()
	goldenLoadOnce.Do(func() { goldenLoaded = loadGoldens(t) })
	v, ok := goldenLoaded[q]
	return v, ok
}
