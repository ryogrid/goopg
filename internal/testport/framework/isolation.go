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
	Path         string
	Sessions     []string
	SetupSQL     string            // global setup run before each permutation
	TeardownSQL  string            // global teardown run after all permutations
	SessionSetup map[string]string // per-session setup run before each permutation
	Steps        map[string]IsolationStep
	Permutations [][]string
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
		Path:         filepath.ToSlash(path),
		Steps:        map[string]IsolationStep{},
		SessionSetup: map[string]string{},
	}

	scanner := bufio.NewScanner(f)
	ctx := ctxTop
	currentSession := ""

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
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
				s.TeardownSQL = body
			}
			continue
		}

		// step
		if m := reStepStart.FindStringSubmatch(line); len(m) >= 5 {
			stepName := m[2]
			if stepName == "" {
				stepName = m[3]
			}
			rest := m[4]
			body := readBlock(rest, scanner)
			session := inferSession(stepName, s.Sessions, currentSession)
			s.Steps[stepName] = IsolationStep{Name: stepName, Session: session, SQL: strings.TrimSpace(body)}
			continue
		}

		// permutation
		if m := rePermutation.FindStringSubmatch(line); len(m) == 2 {
			s.Permutations = append(s.Permutations, parsePermutationTokens(m[1]))
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
func readBlock(rest string, scanner *bufio.Scanner) string {
	var body strings.Builder
	if idx := strings.Index(rest, "}"); idx >= 0 {
		body.WriteString(strings.TrimSpace(rest[:idx]))
		return body.String()
	}
	body.WriteString(strings.TrimSpace(rest))
	for scanner.Scan() {
		next := scanner.Text()
		if idx := strings.Index(next, "}"); idx >= 0 {
			part := strings.TrimSpace(next[:idx])
			if part != "" {
				if body.Len() > 0 {
					body.WriteString("\n")
				}
				body.WriteString(part)
			}
			break
		}
		part := strings.TrimSpace(next)
		if part != "" {
			if body.Len() > 0 {
				body.WriteString("\n")
			}
			body.WriteString(part)
		}
	}
	return body.String()
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
