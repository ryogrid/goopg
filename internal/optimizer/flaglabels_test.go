package optimizer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// M0127-P5.9-q, 2026-08-06 — the tests that tie a benchmark artefact's
// provenance label to the default it names.
//
// The defect these guard has shipped twice, identically: a planner default was
// flipped (GOOPG_RELSIZE_FALLBACK at M0125-0005, GOOPG_PGSHAPED_DP at
// M0127-P5.9) while the gate script's hand-written `unset(off)` label survived,
// so every artefact captured afterwards stated the OPPOSITE of the regime it
// measured — including the acceptance run of the flip itself. Nothing compiled,
// ran, or diffed could notice, because the label lived in a shell printf and
// the default lived in Go.

const flagProvenanceEnvPath = "../../scripts/planner-flags.env"

// TestFlagProvenanceEnvIsGenerated is the tie itself: the checked-in shell
// fragment the gates SOURCE must be exactly what the current Go defaults
// render. Flip a default without regenerating and this fails — which is the
// bar M0127-P5.9-q was filed with.
func TestFlagProvenanceEnvIsGenerated(t *testing.T) {
	got, err := os.ReadFile(flagProvenanceEnvPath)
	if err != nil {
		t.Fatalf("read %s: %v", flagProvenanceEnvPath, err)
	}
	want := RenderFlagProvenanceEnv()
	if string(got) != want {
		t.Errorf("%s is stale — a planner default moved and the gate labels did not.\n"+
			"Regenerate:  go run ./cmd/gen-planner-flag-labels > scripts/planner-flags.env\n\n"+
			"--- on disk ---\n%s\n--- from the Go defaults ---\n%s",
			flagProvenanceEnvPath, got, want)
	}
}

// TestFlagLabelsRoundTrip asserts the property that makes a label useful rather
// than merely present: the token inside `unset(…)` can be exported verbatim and
// reproduces the unset arm. An artefact stamped `GOOPG_PGSHAPED_DP=unset(on)`
// is then a runnable instruction, not prose.
func TestFlagLabelsRoundTrip(t *testing.T) {
	for _, f := range FlagProvenanceTable() {
		if f.Retired {
			continue
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(f.Unset, "unset("), ")")
		if inner == f.Unset || inner == "" {
			t.Errorf("%s: label %q is not of the form unset(<state>)", f.Env, f.Unset)
			continue
		}
		resolve, ok := flagResolvedState[f.Env]
		if !ok {
			t.Errorf("%s: no resolver", f.Env)
			continue
		}
		if got := resolve(inner); got != inner {
			t.Errorf("%s: label says %q but exporting %s=%s resolves to %q — "+
				"the artefact's own instruction does not reproduce its arm",
				f.Env, f.Unset, f.Env, inner, got)
		}
	}
}

// TestFlagProvenanceTableIsResolvable keeps the three declarations that make up
// the table in agreement: every stamped name resolves or is retired, and no
// resolver is orphaned by being dropped from the stamp order.
func TestFlagProvenanceTableIsResolvable(t *testing.T) {
	inOrder := map[string]bool{}
	for _, env := range flagProvenanceOrder {
		if inOrder[env] {
			t.Errorf("%s appears twice in flagProvenanceOrder", env)
		}
		inOrder[env] = true
		_, resolvable := flagResolvedState[env]
		_, retired := flagProvenanceRetired[env]
		if !resolvable && !retired {
			t.Errorf("%s is stamped but has neither a resolver nor a retirement", env)
		}
	}
	for env := range flagResolvedState {
		if !inOrder[env] {
			t.Errorf("%s has a resolver but is never stamped", env)
		}
	}
}

// goopgEnvName matches a GOOPG_* variable name. It is applied to the CONTENTS
// of string literals recovered from the AST, never to raw source — see
// plannerEnvNames.
var goopgEnvName = regexp.MustCompile(`^GOOPG_[A-Z0-9_]+$`)

// plannerEnvNames returns every GOOPG_* name that appears as a string literal
// in the package's non-test sources, with the file it appeared in.
//
// It walks the AST rather than matching `os.Getenv("GOOPG_…")` in raw text,
// because the regex form had a hole that was silently exploited for an entire
// milestone: GOOPG_INDEX_PROBE_MULT is read through the envFloatDefault helper
// (cost_funcs.go), not a literal os.Getenv call, so the detector never saw it
// and the flag that multiplies every NL-index probe cost was absent from every
// benchmark artefact this table exists to stamp. Any read path — direct,
// helper-wrapped, or one this package has not invented yet — puts the name in a
// string literal, so that is what the detector looks at.
//
// Matching literals rather than raw text also keeps flag names quoted in
// COMMENTS out of the result (flaglabels.go's own doc comments quote several),
// which a raw-text regex would report as reads.
func plannerEnvNames(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package dir: %v", err)
	}
	found := map[string]string{}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil || !goopgEnvName.MatchString(v) {
					return true
				}
				if _, seen := found[v]; !seen {
					found[v] = filepath.Base(name)
				}
				return true
			})
		}
	}
	return found
}

// TestFlagProvenanceTableCoversPlannerEnv fails when a plan-shaping env flag is
// added to the planner and named in neither the stamp nor the exemption list.
// Without this, the table would go stale the same silent way the printf did:
// the six flags this table gained at M0127-P5.9-q had been readable by the
// planner — and absent from every artefact — for entire milestones, and
// GOOPG_INDEX_PROBE_MULT then repeated it through a detector hole.
func TestFlagProvenanceTableCoversPlannerEnv(t *testing.T) {
	stamped := map[string]bool{}
	for _, f := range FlagProvenanceTable() {
		stamped[f.Env] = true
	}
	for env, file := range plannerEnvNames(t) {
		if stamped[env] {
			continue
		}
		if _, ok := flagProvenanceExempt[env]; ok {
			continue
		}
		t.Errorf("%s names %s, which no benchmark artefact names.\n"+
			"Add it to flagProvenanceOrder+flagResolvedState (and regenerate "+
			"scripts/planner-flags.env), or to flagProvenanceExempt with the reason "+
			"it cannot change a plan.", file, env)
	}
}

// TestFlagProvenanceDetectorSeesHelperWrappedReads pins the hole itself. The
// previous detector matched only a literal os.Getenv call, so a flag read
// through a helper was invisible to the guard above. GOOPG_INDEX_PROBE_MULT is
// exactly that shape (cost_funcs.go reads it via os.Getenv into
// indexProbeMultFromEnv, and earlier via envFloatDefault), so its presence in
// the detector's output is the regression test for the detector.
func TestFlagProvenanceDetectorSeesHelperWrappedReads(t *testing.T) {
	names := plannerEnvNames(t)
	if _, ok := names["GOOPG_INDEX_PROBE_MULT"]; !ok {
		t.Fatal("detector did not see GOOPG_INDEX_PROBE_MULT; the helper-wrapped-read hole is back")
	}
}

// TestGateScriptsUseGeneratedFlagLabels stops the labels from creeping back
// into the shell. A gate that hand-writes `unset(...)` again is outside every
// guard above, which is exactly the state this task found.
func TestGateScriptsUseGeneratedFlagLabels(t *testing.T) {
	for _, script := range []string{
		"../../scripts/tpcds-sf05-regression.sh",
		"../../scripts/tpch-spotcheck.sh",
	} {
		src, err := os.ReadFile(script)
		if err != nil {
			t.Fatalf("read %s: %v", script, err)
		}
		text := string(src)
		if !strings.Contains(text, "planner-flags.sh") {
			t.Errorf("%s does not source scripts/planner-flags.sh — its provenance "+
				"stamp is not tied to the Go defaults", filepath.Base(script))
		}
		for i, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue // the comment blocks quote the historical labels on purpose
			}
			if strings.Contains(trimmed, "unset(") {
				t.Errorf("%s:%d hand-writes a provenance label: %s",
					filepath.Base(script), i+1, trimmed)
			}
		}
	}
}
