Task: M-NIGHTLY — nightly DISCARDS every regress diff (infrastructure fix)

Files:
  - ci/batch/stages/stage-testport.sh: export GOOPG_REGRESS_DIFF_DIR + mkdir

Key symbols: GOOPG_REGRESS_DIFF_DIR, internal/testport/framework/regress.go

Hypothesis/Findings:
  - The regress framework already writes per-case diff files when
    GOOPG_REGRESS_DIFF_DIR is set. The gap was purely that the nightly stage
    never exported it. One-line fix: create the dir and export the env var.
  - The full set of milestone priorities M0124→M0125→M0127→M0128 are all
    CLOSED. M-NIGHTLY is now un-parked and selectable.
  - Two new nightly items (AI-20260808-005620-001 EvalPlanQual, -002 Q95-timeout)
    were already filed in fix_plan.md by the nightly system.

Next step: Continue with M-NIGHTLY or M0123-S4. Highest-impact remaining
  unchecked M-NIGHTLY items: graceful-shutdown hang, regress/suite-wedge,
  regress output-normalization groups. M0123-S4 sub-slice 39 (timetz(N))
  is a well-understood follow-on from sub-slice 38.

Gates run: ralph-state-guard OK (auto-repaired progress inconsistency)

In-flight: none
