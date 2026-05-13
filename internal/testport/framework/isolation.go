package framework

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type IsolationStep struct {
	Name    string
	Session string
	SQL     string
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
	rePermutation  = regexp.MustCompile(`^permutation\s+(.+)$`)
	reQuotedTokens = regexp.MustCompile(`"([^"]+)"`)
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
			s.Steps[stepNameParsed] = IsolationStep{Name: stepNameParsed, Session: session, SQL: strings.TrimSpace(body)}
			continue
		}

		// permutation — may span multiple lines; continuation lines are indented
		if m := rePermutation.FindStringSubmatch(line); len(m) == 2 {
			tokens := parsePermutationTokens(m[1])
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
				if !isIndented || stripped == "" {
					// Not a continuation — push back and stop.
					pendingLine = nextRaw
					break
				}
				tokens = append(tokens, parsePermutationTokens(stripped)...)
			}
			s.Permutations = append(s.Permutations, tokens)
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
	// Multi-line: read lines until closing brace, preserving raw content.
	var lines []string
	if t := strings.TrimSpace(rest); t != "" {
		lines = append(lines, t)
	}
	for scanner.Scan() {
		next := scanner.Text()
		if idx := strings.Index(next, "}"); idx >= 0 {
			if t := strings.TrimSpace(next[:idx]); t != "" {
				lines = append(lines, t)
			}
			break
		}
		lines = append(lines, next) // preserve raw line with indentation
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
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

func parsePermutationTokens(raw string) []string {
	quoted := reQuotedTokens.FindAllStringSubmatch(raw, -1)
	if len(quoted) > 0 {
		out := make([]string, 0, len(quoted))
		for _, q := range quoted {
			out = append(out, q[1])
		}
		return out
	}
	fields := strings.Fields(raw)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(strings.Trim(f, `"`))
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
