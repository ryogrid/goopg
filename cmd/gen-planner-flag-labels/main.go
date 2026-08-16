// Command gen-planner-flag-labels writes the shell fragment the benchmark
// gates source to stamp their artefacts with the planner flags in force
// (`scripts/planner-flags.env`).
//
// M0127-P5.9-q, 2026-08-06. The gates used to hand-write the label for an UNSET
// variable inside a printf, and nothing tied those strings to the Go defaults
// they claimed to describe. Two default flips shipped with the old label
// intact — GOOPG_RELSIZE_FALLBACK at M0125-0005 and GOOPG_PGSHAPED_DP at
// M0127-P5.9 — so every artefact captured after each flip stated the OPPOSITE
// of the regime it measured. The labels now come from
// internal/planner.RenderFlagProvenanceEnv, which derives each one from the
// same function that resolves the default at process start.
//
// Usage:
//
//	go run ./cmd/gen-planner-flag-labels > scripts/planner-flags.env
//
// The generated file is CHECKED IN so a gate can stamp an artefact on a host
// with no Go toolchain, and TestFlagProvenanceEnvIsGenerated fails when it
// drifts from the defaults.
package main

import (
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/optimizer"
)

func main() {
	if _, err := fmt.Fprint(os.Stdout, optimizer.RenderFlagProvenanceEnv()); err != nil {
		fmt.Fprintf(os.Stderr, "gen-planner-flag-labels: %v\n", err)
		os.Exit(1)
	}
}
