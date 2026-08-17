package optimizer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Flag provenance — the table the benchmark gates stamp their artefacts with,
// derived from the live Go defaults rather than restated by hand.
//
// WHY THIS FILE EXISTS (M0127-P5.9-q, 2026-08-06). Both benchmark gates print a
// `planner-flags:` line into every report they write, so that a diff between
// two captures is attributable to a known arm instead of to the operator's
// memory of what was exported. For a variable that IS exported the line is
// trivially true; the interesting case is the UNSET one, where the line has to
// state what the binary does by default — and that half was a hand-written
// string sitting in a shell `printf`, checked by nothing.
//
// It has now shipped wrong twice, the same way both times:
//
//   - M0125-0005 flipped `GOOPG_RELSIZE_FALLBACK`'s default from off to stage 2
//     and the `unset(off)` label survived the flip. Every artefact captured
//     afterwards stated the OPPOSITE of the regime it measured. Fixed by hand,
//     and the fix's own comment predicted the recurrence.
//   - M0127-P5.9 flipped `GOOPG_PGSHAPED_DP` on and `unset(off)` survived
//     again, mis-stamping `sweep-20260806-022814.txt` and
//     `plans-20260806-022814.txt` — the acceptance run of the flip itself.
//
// A mis-stamped artefact is worse than an unstamped one: it is the record a
// later loop reads to decide what an A/B measured, and P5.9-n lost a turn to
// exactly that. So the labels move out of the shell and become a value computed
// from the same functions that resolve the defaults at process start
// (`pgShapedDPFromEnv`, `parseRelSizeFallbackStage`, …). Flipping a default now
// changes the rendered table, which no longer matches the checked-in
// `scripts/planner-flags.env`, which fails `TestFlagProvenanceEnvIsGenerated`.
// The label cannot survive a flip it does not agree with.

// FlagProvenance is one row of the table: an env var whose value shapes plans,
// and the label an artefact carries when that var is UNSET.
type FlagProvenance struct {
	// Env is the environment variable name, e.g. "GOOPG_PGSHAPED_DP".
	Env string
	// Unset is what the gate stamps when the variable is not exported. It
	// spells the RESOLVED default — `unset(on)`, `unset(off)`, `unset(2)` —
	// so the line is a positive statement about the binary rather than a
	// note that the operator typed nothing.
	Unset string
	// Retired marks a variable no code reads any more. The gate stamps the
	// retirement instead of the environment, so a later loop cannot A/B a
	// variable the binary cannot see. The row is kept rather than deleted
	// because dropping it would make older artefacts that DO carry the
	// variable look like this version of the gate produced them.
	Retired bool
}

// onOff spells a boolean flag's resolved state. It describes the VARIABLE, never
// the feature — so an inverted switch such as GOOPG_PGSHAPED_COLLAPSE resolves
// to `off` ("the off-switch is not engaged"). Reading it the other way would
// make one row of the stamp mean the opposite of its neighbours. (The pattern's
// original exemplar, GOOPG_MHJ_PACKING_OFF, was retired by M0127-P6.2.)
func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// flagResolvedState renders what the binary DOES for a given value of a given
// flag, using the same function that resolves it at process start. It is what
// makes the label round-trippable: the token inside `unset(…)` can be exported
// verbatim to reproduce the unset arm, which `TestFlagLabelsRoundTrip` asserts
// for every row.
//
// Nothing here may be a literal that restates a default declared elsewhere —
// that duplication IS the defect this file exists to make impossible.
var flagResolvedState = map[string]func(string) string{
	"GOOPG_RELSIZE_FALLBACK":  func(v string) string { return strconv.Itoa(parseRelSizeFallbackStage(v)) },
	"GOOPG_MEMOIZE":           func(v string) string { return onOff(memoizeFromEnv(v)) },
	"GOOPG_PARALLEL":          func(v string) string { return onOff(parallelFromEnv(v)) },
	"GOOPG_PGSHAPED_DP":       func(v string) string { return onOff(pgShapedDPFromEnv(v)) },
	"GOOPG_PGSHAPED_COLLAPSE": func(v string) string { return onOff(pgShapedCollapseFromEnv(v)) },
	"GOOPG_EXISTS_TO_ANY":     func(v string) string { return onOff(existsToAnyFromEnv(v)) },
	"GOOPG_UNNEST_PREDP":      func(v string) string { return onOff(unnestPreDPFromEnv(v)) },
	"GOOPG_INDEXKEY_HARVEST":  func(v string) string { return onOff(indexKeyHarvestFromEnv(v)) },
	"GOOPG_HASH_OUTER_JOIN":   func(v string) string { return onOff(hashOuterJoinFromEnv(v)) },
	// A mode, not a boolean: the artefact carries the word an operator would
	// export to reproduce the arm.
	"GOOPG_NLI_COSTGATE": func(v string) string {
		if nliCostGateLegacyFromEnv(v) {
			return "legacy"
		}
		return "current"
	},
}

// flagProvenanceOrder is the order the flags are stamped in. The first six are
// the ones the SF0.5 gate already named, kept in place so a new capture diffs
// cleanly against the corpus of older ones; the rest joined at M0127-P5.9-q,
// when the table made naming them free.
var flagProvenanceOrder = []string{
	"GOOPG_RELSIZE_FALLBACK",
	"GOOPG_COST_DRIVEN_JOINORDER",
	"GOOPG_MEMOIZE",
	"GOOPG_PARALLEL",
	"GOOPG_PGSHAPED_DP",
	"GOOPG_PGSHAPED_COLLAPSE",
	"GOOPG_EXISTS_TO_ANY",
	"GOOPG_UNNEST_PREDP",
	"GOOPG_INDEXKEY_HARVEST",
	"GOOPG_NLI_COSTGATE",
	"GOOPG_HASH_OUTER_JOIN",
	"GOOPG_MHJ_PACKING_OFF",
	"GOOPG_GS_SHARE_SOURCE",
	// Joined at M0125-0040: grouping-sets source sharing changes the plan of
	// every ROLLUP/CUBE/GROUPING SETS query, so a TPC-DS artefact that does
	// not name it cannot say which arm it measured.
}

// flagProvenanceRetired names variables no code reads any more, and the
// milestone that retired them. `GOOPG_COST_DRIVEN_JOINORDER` was the M0126
// cost-driven join-order switch; M0127-P5.9 made the PG-shaped search the
// default enumerator and nothing reads the old variable
// (internal/planner/bushy.go:13). The row survives its retirement because
// dropping it would make older artefacts that DO carry it look like they came
// from this version of the gate.
var flagProvenanceRetired = map[string]string{
	"GOOPG_COST_DRIVEN_JOINORDER": "M0127-P5.9",
	// M0126-0005's measurement-only switch for forcing MultiHashJoin packing
	// off independently of join-order. M0127-P6.2 deleted the packer, so the
	// off-switch has nothing left to turn off.
	"GOOPG_MHJ_PACKING_OFF": "M0127-P6.2",
	// M0125-0040's grouping-sets source-sharing knob. M0125-0048 replaced the
	// UNION-ALL expansion the knob existed to make cheaper with a single-pass
	// grouping-sets aggregate, so there is no source to share and nothing
	// reads the variable.
	"GOOPG_GS_SHARE_SOURCE": "M0125-0048",
}

// FlagProvenanceTable is the authoritative list of planner env flags that a
// benchmark artefact must name, in the order they are stamped.
func FlagProvenanceTable() []FlagProvenance {
	out := make([]FlagProvenance, 0, len(flagProvenanceOrder))
	for _, env := range flagProvenanceOrder {
		if ms, ok := flagProvenanceRetired[env]; ok {
			out = append(out, FlagProvenance{Env: env, Unset: "retired(" + ms + ")", Retired: true})
			continue
		}
		resolve, ok := flagResolvedState[env]
		if !ok {
			// Unreachable in a built binary — TestFlagProvenanceTableIsResolvable
			// fails first — but a table row with no resolver would otherwise
			// stamp an empty label, which is the silent half of this defect.
			out = append(out, FlagProvenance{Env: env, Unset: "unset(?)"})
			continue
		}
		out = append(out, FlagProvenance{Env: env, Unset: "unset(" + resolve("") + ")"})
	}
	return out
}

// flagProvenanceExempt lists `GOOPG_*` variables read by this package that a
// plan-provenance line deliberately omits, with the reason. The completeness
// test (`TestFlagProvenanceTableCoversPlannerEnv`) fails on any planner env var
// that is in neither the table nor this map, so a new plan-shaping flag cannot
// be added without a decision about whether artefacts must name it.
var flagProvenanceExempt = map[string]string{
	"GOOPG_PGSHAPED_DP_TRACE":  "diagnostic only: emits the enumeration trace, never changes a chosen plan",
	"GOOPG_NLI_COSTGATE_DEBUG": "diagnostic only: logs the NLI cost-gate decision, never changes it",
}

// shellSingleQuote quotes s for POSIX sh. The labels are ASCII today, but a
// generated file that a gate SOURCES must not be able to execute anything.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RenderFlagProvenanceEnv renders the table as the shell fragment the gates
// source (`scripts/planner-flags.env`). It is the single writer: both
// `cmd/gen-planner-flag-labels` and the test that guards the checked-in file
// call this, so the file on disk can never drift from the Go defaults without
// the test noticing.
//
// The format is deliberately dumb — flat assignments plus an ordered name list
// — because the consumers are bash gate scripts that must keep working when Go
// is not installed on the host that reruns an old capture.
func RenderFlagProvenanceEnv() string {
	var b strings.Builder
	b.WriteString("# GENERATED by cmd/gen-planner-flag-labels — DO NOT EDIT.\n")
	b.WriteString("#\n")
	b.WriteString("# Regenerate with:  go run ./cmd/gen-planner-flag-labels > scripts/planner-flags.env\n")
	b.WriteString("#\n")
	b.WriteString("# The unset-labels below are computed from the planner's own default\n")
	b.WriteString("# resolvers (internal/planner/flaglabels.go), so a flipped default that is\n")
	b.WriteString("# not regenerated here fails TestFlagProvenanceEnvIsGenerated. Two mis-stamped\n")
	b.WriteString("# artefact generations (M0125-0005, M0127-P5.9) are why this is not a printf.\n")
	b.WriteString("\n")

	tbl := FlagProvenanceTable()
	names := make([]string, 0, len(tbl))
	for _, f := range tbl {
		names = append(names, f.Env)
	}
	fmt.Fprintf(&b, "GOOPG_PLANNER_FLAG_VARS=%s\n", shellSingleQuote(strings.Join(names, " ")))
	for _, f := range tbl {
		fmt.Fprintf(&b, "GOOPG_PLANNER_FLAG_LABEL_%s=%s\n", f.Env, shellSingleQuote(f.Unset))
		if f.Retired {
			fmt.Fprintf(&b, "GOOPG_PLANNER_FLAG_RETIRED_%s=1\n", f.Env)
		}
	}
	return b.String()
}

// FlagProvenanceExemptNames returns the exempt variable names, sorted. Test
// helper kept next to the map so the two cannot drift.
func FlagProvenanceExemptNames() []string {
	out := make([]string, 0, len(flagProvenanceExempt))
	for k := range flagProvenanceExempt {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
