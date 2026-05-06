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
	reSession      = regexp.MustCompile(`^session\s+"([^"]+)"\s*$`)
	reStepStart    = regexp.MustCompile(`^step\s+"([^"]+)"\s*\{\s*(.*)$`)
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

// ParseIsolationSpec parses enough of PostgreSQL isolation spec syntax to run
// deterministic step permutations for harness validation.
func ParseIsolationSpec(path string) (IsolationSpec, error) {
	f, err := os.Open(path)
	if err != nil {
		return IsolationSpec{}, fmt.Errorf("open spec %q: %w", path, err)
	}
	defer f.Close()

	s := IsolationSpec{Path: filepath.ToSlash(path), Steps: map[string]IsolationStep{}}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := reSession.FindStringSubmatch(line); len(m) == 2 {
			s.Sessions = append(s.Sessions, m[1])
			continue
		}
		if m := reStepStart.FindStringSubmatch(line); len(m) == 3 {
			stepName := m[1]
			rest := m[2]
			var body strings.Builder
			if strings.Contains(rest, "}") {
				body.WriteString(strings.TrimSpace(strings.SplitN(rest, "}", 2)[0]))
			} else {
				body.WriteString(strings.TrimSpace(rest))
				for scanner.Scan() {
					next := scanner.Text()
					if idx := strings.Index(next, "}"); idx >= 0 {
						body.WriteString("\n")
						body.WriteString(strings.TrimSpace(next[:idx]))
						break
					}
					body.WriteString("\n")
					body.WriteString(strings.TrimSpace(next))
				}
			}
			session := inferSession(stepName, s.Sessions)
			s.Steps[stepName] = IsolationStep{Name: stepName, Session: session, SQL: strings.TrimSpace(body.String())}
			continue
		}
		if m := rePermutation.FindStringSubmatch(line); len(m) == 2 {
			s.Permutations = append(s.Permutations, parsePermutationTokens(m[1]))
		}
	}
	if err := scanner.Err(); err != nil {
		return IsolationSpec{}, fmt.Errorf("scan spec %q: %w", path, err)
	}
	return s, nil
}

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
			case strings.Contains(strings.ToLower(err.Error()), "defer"):
				results = append(results, IsolationStepResult{StepName: stepName, Session: step.Session, Status: "defer", Reason: err.Error()})
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

func inferSession(stepName string, sessions []string) string {
	for _, s := range sessions {
		if strings.HasPrefix(stepName, s+"_") || strings.HasPrefix(stepName, s+"-") || strings.HasPrefix(stepName, s) {
			return s
		}
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
