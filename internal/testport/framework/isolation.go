package framework

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type IsolationStep struct {
	Name    string
	Session string
	SQL     string
}

// BlockerKind classifies a permutation-step completion blocker, mirroring the
// parenthesised markers documented in postgres/src/test/isolation/README.
// A blocker only delays *reporting* a step as completed; it never delays the
// launch of a step.
type BlockerKind int

const (
	// BlockerStepComplete delays the annotated step's completion report until
	// the referenced step has completed. Marker form: "<other step name>".
	BlockerStepComplete BlockerKind = iota
	// BlockerNotices delays the annotated step's completion report until the
	// referenced step's session has emitted at least Count NOTICE messages,
	// counted from when the annotated step is launched. Marker form:
	// "<other step name> notices <n>".
	BlockerNotices
	// BlockerStar forces the annotated step to be reported as waiting as soon
	// as it is launched, regardless of later detection. Marker form: "*".
	BlockerStar
)

// StepBlocker is one parenthesised marker attached to a permutation step.
type StepBlocker struct {
	Kind     BlockerKind
	StepName string // referenced step name (empty for BlockerStar)
	Count    int    // NOTICE threshold (BlockerNotices only)
}

type IsolationSpec struct {
	Path             string
	Sessions         []string
	SetupSQL         string            // global setup run before each permutation
	TeardownSQL      string            // global teardown run after each permutation
	SessionSetup     map[string]string // per-session setup run before each permutation
	SessionTeardown  map[string]string // per-session teardown run after each permutation
	Steps            map[string]IsolationStep
	StepOrder        []string          // step names in declaration order (for "unused step" warnings)
	Permutations     [][]string
	// PermutationBlockers is parallel to Permutations: PermutationBlockers[p][i]
	// holds the completion blockers attached to Permutations[p][i]. Steps with
	// no markers have a nil slice. Always the same outer/inner length as
	// Permutations so it can be indexed in lockstep.
	PermutationBlockers [][][]StepBlocker
}

type IsolationStepResult struct {
	StepName   string
	Session    string
	Status     string
	Reason     string
	Normalized string
}

type IsolationExecutor interface {
	ExecuteStep(ctx context.Context, session string, sql string) (string, error)
}

var (
	reSession      = regexp.MustCompile(`^session\s+("([^"]+)"|(\S+))\s*$`)
	reStepStart    = regexp.MustCompile(`^step\s+("([^"]+)"|(\S+))\s*\{(.*)$`)
	reStepNoBlock  = regexp.MustCompile(`^step\s+("([^"]+)"|(\S+))\s*$`)
	rePermutation  = regexp.MustCompile(`^permutation(?:\s+(.+))?$`)
)

// DiscoverIsolationSpecs returns all upstream isolation .spec files.
func DiscoverIsolationSpecs(repoRoot string) ([]string, error) {
	glob := filepath.Join(repoRoot, "postgres", "src", "test", "isolation", "specs", "*.spec")
	matches, err := filepath.Glob(glob)
	if err != nil {
		return nil, fmt.Errorf("glob isolation specs: %w", err)
	}
	sort.Strings(matches)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, err := filepath.Rel(repoRoot, m)
		if err != nil {
			return nil, fmt.Errorf("rel isolation path: %w", err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

type parseContext int

const (
	ctxTop parseContext = iota
	ctxSession
)

// ParseIsolationSpec parses a PostgreSQL isolation spec file.
// Supports global setup/teardown, per-session setup, steps, and permutations.
func ParseIsolationSpec(path string) (IsolationSpec, error) {
	f, err := os.Open(path)
	if err != nil {
		return IsolationSpec{}, fmt.Errorf("open spec %q: %w", path, err)
	}
	defer f.Close()

	s := IsolationSpec{
		Path:            filepath.ToSlash(path),
		Steps:           map[string]IsolationStep{},
		SessionSetup:    map[string]string{},
		SessionTeardown: map[string]string{},
	}

	scanner := bufio.NewScanner(f)
	ctx := ctxTop
	currentSession := ""

	// pendingLine holds a raw line that was read ahead but not yet consumed.
	pendingLine := ""

	nextLine := func() (string, bool) {
		if pendingLine != "" {
			l := pendingLine
			pendingLine = ""
			return l, true
		}
		if scanner.Scan() {
			return scanner.Text(), true
		}
		return "", false
	}

	for {
		rawLine, ok := nextLine()
		if !ok {
			break
		}
		line := strings.TrimSpace(rawLine)
		// strip inline comments
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}

		// Session declaration
		if m := reSession.FindStringSubmatch(line); len(m) >= 2 {
			name := m[2]
			if name == "" {
				name = m[3]
			}
			s.Sessions = append(s.Sessions, name)
			ctx = ctxSession
			currentSession = name
			continue
		}

		// setup block
		if strings.HasPrefix(line, "setup") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "setup"))
			if rest == "" {
				// Opening brace on the next line.
				rest = nextNonEmpty(scanner)
			}
			if strings.HasPrefix(rest, "{") {
				body := readBlock(rest[1:], scanner)
				if ctx == ctxSession && currentSession != "" {
					s.SessionSetup[currentSession] = body
				} else {
					s.SetupSQL = body
				}
			}
			continue
		}

		// teardown block
		if strings.HasPrefix(line, "teardown") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "teardown"))
			if rest == "" {
				rest = nextNonEmpty(scanner)
			}
			if strings.HasPrefix(rest, "{") {
				body := readBlock(rest[1:], scanner)
				if ctx == ctxSession && currentSession != "" {
					s.SessionTeardown[currentSession] = body
				} else {
					s.TeardownSQL = body
				}
			}
			continue
		}

		// step — handles both inline-brace (`step name {`) and next-line brace
		// (`step name\n{`), and both quoted (`step "name"`) and unquoted names.
		var stepNameParsed, stepRest string
		if m := reStepStart.FindStringSubmatch(line); len(m) >= 5 {
			if m[2] != "" {
				stepNameParsed = m[2]
			} else {
				stepNameParsed = m[3]
			}
			stepRest = m[4]
		} else if m := reStepNoBlock.FindStringSubmatch(line); len(m) >= 2 {
			if m[2] != "" {
				stepNameParsed = m[2]
			} else {
				stepNameParsed = m[3]
			}
			// Brace is on the next line — advance scanner until we find it.
			for scanner.Scan() {
				next := strings.TrimSpace(scanner.Text())
				if next == "" {
					continue
				}
				if strings.HasPrefix(next, "{") {
					stepRest = next[1:]
					break
				}
				// Unexpected token — not a step block; skip.
				stepNameParsed = ""
				break
			}
		}
		if stepNameParsed != "" {
			body := readBlock(stepRest, scanner)
			session := inferSession(stepNameParsed, s.Sessions, currentSession)
			if _, exists := s.Steps[stepNameParsed]; !exists {
				s.StepOrder = append(s.StepOrder, stepNameParsed)
			}
			// Preserve the verbatim block body. Leading whitespace on
			// the first content line (and leading `\n` for brace-at-EOL
			// layouts) is significant for multi-line SQL display; a
			// trailing `\n` (when `}` sits on its own line) is significant
			// so a follow-on `<waiting ...>` suffix appears on a fresh
			// line. See readBlock for the format-parity rationale.
			s.Steps[stepNameParsed] = IsolationStep{Name: stepNameParsed, Session: session, SQL: body}
			continue
		}

		// permutation — may span multiple lines; continuation lines are indented
		if m := rePermutation.FindStringSubmatch(line); len(m) == 2 {
			rawParts := make([]string, 0, 4)
			if strings.TrimSpace(m[1]) != "" {
				rawParts = append(rawParts, m[1])
			}
			// Read continuation lines (indented lines with only step names).
			for {
				nextRaw, ok2 := nextLine()
				if !ok2 {
					break
				}
				// Continuation lines start with whitespace in the original file.
				isIndented := len(nextRaw) > 0 && (nextRaw[0] == ' ' || nextRaw[0] == '\t')
				stripped := strings.TrimSpace(nextRaw)
				if idx := strings.Index(stripped, "#"); idx >= 0 {
					stripped = strings.TrimSpace(stripped[:idx])
				}
				// Blank lines (isIndented=false, stripped="") within a
				// multi-line permutation are treated as whitespace and
				// skipped — upstream specscanner.l tokenises blank lines
				// inside a permutation block as whitespace, not terminators.
				// Only a non-blank, non-indented line terminates the block.
				if !isIndented && stripped != "" {
					// Non-blank, non-indented line — not a continuation.
					pendingLine = nextRaw
					break
				}
				if stripped == "" {
					// Blank line or indented comment-only line — skip.
					continue
				}
				rawParts = append(rawParts, stripped)
			}
			// Parse the joined permutation body in one pass so parenthesised
			// markers can attach to the preceding step even across line breaks.
			steps, blockers := parsePermutation(strings.Join(rawParts, " "))
			s.Permutations = append(s.Permutations, steps)
			s.PermutationBlockers = append(s.PermutationBlockers, blockers)
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return IsolationSpec{}, fmt.Errorf("scan spec %q: %w", path, err)
	}
	return s, nil
}

// nextNonEmpty returns the next non-empty, non-comment trimmed line from scanner.
func nextNonEmpty(scanner *bufio.Scanner) string {
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line != "" {
			return line
		}
	}
	return ""
}

// readBlock reads the content inside a { ... } block from the scanner.
// rest is the text after the opening '{' on the same line.
// Raw indentation is preserved so that multi-line SQL prints exactly as
// written in the spec file (matching PostgreSQL isolationtester output).
func readBlock(rest string, scanner *bufio.Scanner) string {
	// Single-line: closing brace is on the same line.
	if idx := strings.Index(rest, "}"); idx >= 0 {
		return strings.TrimSpace(rest[:idx])
	}
	// Multi-line: read lines until closing brace.
	//
	// Upstream isolationtester (`specscanner.l`, rules for `{`/`}` with
	// `{space}*` = `[ \t\r\f]*`) captures everything between the opening
	// brace and the next horizontal-whitespace-before-`}` verbatim, INCLUDING
	// embedded newlines. Trailing horizontal whitespace immediately before
	// `}` is eaten, but a `\n` right before `}` is preserved in the buffer.
	//
	// Concretely:
	//   - Opening `{` at end-of-line: the first byte of the body is `\n`,
	//     which makes the runner's `step name: %s` format render as
	//     `step name: \n<body>` (i.e. body starts on the next line).
	//   - Closing `}` on its own line (or after only horizontal whitespace
	//     on that line): the body ends with `\n`, which makes the runner's
	//     `step name: %s <waiting ...>` format render with `<waiting ...>`
	//     on a fresh line (with a single leading space from the format
	//     string). This is the merge-match-recheck shape — see
	//     `postgres/src/test/isolation/expected/merge-match-recheck.out`.
	//   - Closing `}` on the same line as the last SQL content (e.g.
	//     `INSERT ... }`) does NOT carry a trailing `\n`, so `<waiting ...>`
	//     stays on the same line as the last SQL line. This is the
	//     insert-conflict-do-update-4 shape.
	openedOnEOL := strings.TrimSpace(rest) == ""
	closedOnOwnLine := false
	var lines []string
	if t := strings.TrimSpace(rest); t != "" {
		lines = append(lines, t)
	}
	for scanner.Scan() {
		next := scanner.Text()
		if idx := strings.Index(next, "}"); idx >= 0 {
			line := strings.TrimRight(next[:idx], " \t")
			if strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			} else {
				closedOnOwnLine = true
			}
			break
		}
		lines = append(lines, next) // preserve raw line with indentation
	}
	if len(lines) == 0 {
		return ""
	}
	joined := strings.Join(lines, "\n")
	if openedOnEOL {
		joined = "\n" + joined
	}
	if closedOnOwnLine {
		joined = joined + "\n"
	}
	return joined
}

// RunIsolationPermutation executes a single permutation sequentially using the
// provided IsolationExecutor. This is the simple sequential harness used for
// framework unit tests; see IsolationRunner for full concurrent execution.
func RunIsolationPermutation(ctx context.Context, spec IsolationSpec, permutationIndex int, exec IsolationExecutor) ([]IsolationStepResult, error) {
	if permutationIndex < 0 || permutationIndex >= len(spec.Permutations) {
		return nil, fmt.Errorf("permutation index %d out of range", permutationIndex)
	}
	perm := spec.Permutations[permutationIndex]
	results := make([]IsolationStepResult, 0, len(perm))
	for _, stepName := range perm {
		step, ok := spec.Steps[stepName]
		if !ok {
			results = append(results, IsolationStepResult{StepName: stepName, Status: "defer", Reason: "step not defined in spec"})
			continue
		}
		out, err := exec.ExecuteStep(ctx, step.Session, step.SQL)
		if err != nil {
			switch {
			case strings.Contains(strings.ToLower(err.Error()), "excluded"):
				results = append(results, IsolationStepResult{StepName: stepName, Session: step.Session, Status: "excluded", Reason: err.Error()})
			default:
				results = append(results, IsolationStepResult{StepName: stepName, Session: step.Session, Status: "defer", Reason: err.Error()})
			}
			continue
		}
		results = append(results, IsolationStepResult{
			StepName:   stepName,
			Session:    step.Session,
			Status:     "port",
			Reason:     "step executed",
			Normalized: NormalizeRegressOutput(out),
		})
	}
	return results, nil
}

func inferSession(stepName string, sessions []string, currentSession string) string {
	if currentSession != "" {
		return currentSession
	}
	for _, s := range sessions {
		if strings.HasPrefix(stepName, s+"_") || strings.HasPrefix(stepName, s+"-") || strings.HasPrefix(stepName, s) {
			return s
		}
	}
	if len(sessions) > 0 {
		return sessions[0]
	}
	return "session1"
}

// permTokenize splits a (comment-stripped, possibly multi-line) permutation
// body into tokens. Double-quoted identifiers are returned without their
// quotes as a single token; the punctuation characters '(', ')' and ',' are
// each returned as standalone tokens; all other runs of non-space,
// non-punctuation characters form bareword tokens. This char-level scan
// handles glued forms such as "mystep(*)".
func permTokenize(raw string) []string {
	var toks []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for _, r := range raw {
		switch {
		case inQuote:
			if r == '"' {
				toks = append(toks, cur.String())
				cur.Reset()
				inQuote = false
			} else {
				cur.WriteRune(r)
			}
		case r == '"':
			flush()
			inQuote = true
		case r == '(' || r == ')' || r == ',':
			flush()
			toks = append(toks, string(r))
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return toks
}

// parsePermutation parses a permutation body into the ordered list of step
// names and a parallel slice of per-step completion blockers. Parenthesised
// markers attach to the most recently seen step, matching PostgreSQL
// isolationtester semantics (postgres/src/test/isolation/README). The returned
// slices always have equal length; steps without markers carry a nil slice.
func parsePermutation(raw string) ([]string, [][]StepBlocker) {
	toks := permTokenize(raw)
	steps := make([]string, 0, len(toks))
	blockers := make([][]StepBlocker, 0, len(toks))
	for i := 0; i < len(toks); {
		t := toks[i]
		switch t {
		case "(":
			grp, next := parseBlockerGroup(toks, i+1)
			if n := len(steps); n > 0 && len(grp) > 0 {
				blockers[n-1] = append(blockers[n-1], grp...)
			}
			i = next
		case ")", ",":
			i++
		default:
			steps = append(steps, t)
			blockers = append(blockers, nil)
			i++
		}
	}
	return steps, blockers
}

// parseBlockerGroup parses comma-separated markers beginning just after '('
// until the matching ')'. It returns the parsed blockers and the index just
// past the ')'. Recognised markers are "*", "<step>", and
// "<step> notices <n>"; unrecognised marker shapes are ignored.
func parseBlockerGroup(toks []string, start int) ([]StepBlocker, int) {
	var out []StepBlocker
	var marker []string
	emit := func() {
		switch {
		case len(marker) == 0:
			// nothing
		case len(marker) == 1 && marker[0] == "*":
			out = append(out, StepBlocker{Kind: BlockerStar})
		case len(marker) == 3 && marker[1] == "notices":
			if n, err := strconv.Atoi(marker[2]); err == nil && n > 0 {
				out = append(out, StepBlocker{Kind: BlockerNotices, StepName: marker[0], Count: n})
			}
		case len(marker) == 1:
			out = append(out, StepBlocker{Kind: BlockerStepComplete, StepName: marker[0]})
		}
		marker = marker[:0]
	}
	i := start
	for i < len(toks) {
		switch toks[i] {
		case ")":
			emit()
			return out, i + 1
		case ",":
			emit()
		default:
			marker = append(marker, toks[i])
		}
		i++
	}
	emit()
	return out, i
}
