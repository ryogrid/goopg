(idle — nothing in flight)

Last loop: **M0127-P5.9-q CLOSED** (09 §3.16) — the gates' `planner-flags:`
provenance stamp is now GENERATED from the Go defaults it names, after the same
hand-written `unset(off)` label outlived two default flips (M0125-0005,
M0127-P5.9) and mis-stamped the acceptance run of the second one.

Chain: `internal/planner/flaglabels.go` (each label computed by the SAME
resolver production uses at process start) → `cmd/gen-planner-flag-labels` →
`scripts/planner-flags.env` (generated, checked in — a gate host needs no Go) →
`scripts/planner-flags.sh: planner_flags_body`, sourced by BOTH
`tpcds-sf05-regression.sh` and `tpch-spotcheck.sh`. Several `FromEnv` helpers
were factored out of their `init()` so nothing restates a default.

Four guards (`internal/planner/flaglabels_test.go`), two verified by NEGATIVE
probe: checked-in env file == what the defaults render (the stated bar; probe:
flipping `pgShapedCollapseFromEnv` fails it); every `unset(<tok>)` round-trips
through the flag's own parser; every `os.Getenv("GOOPG_*")` in the package is
stamped or exempt with a reason (probe: a scratch flag fails it); neither gate
may hand-write `unset(`.

Coverage guard's first finding: the stamp named **6** flags, the planner reads
**12**. EXISTS_TO_ANY / UNNEST_PREDP / INDEXKEY_HARVEST / NLI_COSTGATE /
HASH_OUTER_JOIN / MHJ_PACKING_OFF were in no artefact ever captured, and
`tpch-spotcheck` — the gate every planner commit pays — named no enumerator
flag at all. The 6 pre-existing labels are byte-identical, so the capture
corpus stays comparable.

The plan channel reproduced the hazard live: its DEFAULT baseline picked the
fixed-binary **OFF** arm and printed `same=13 changed=86` (an exact replication
of §3.15's flip blast radius) — only the header says which arm that was.

NEXT LOOP (subject to the fix_plan `## Current Priority` banner, which wins):
M0127-P5.9 successors remain — **-m** (collapse-ON acceptance pass, gates the
COLLAPSE flip; it runs the SF0.5 sweep this loop deferred), **-o** (EXPLAIN
prints no `Join Filter:` line), **-p** (searched-arm batch-growth fixture).

Nightly triage: tonight's action-items file is the SAME run (20260806-011323)
the previous loop already filed as an M-NIGHTLY harness item (14 of its 18 AI
items are phantom testport regressions from one compile error in a dirty-tree
build). Nothing new to file.

Gates run: `go build ./...`; `go vet ./internal/planner ./cmd/gen-planner-flag-labels`;
`bash -n` on all three scripts; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS; `scripts/tpch-spotcheck.sh` **PASS**
(Q12=2, Q13=35); SF0.5 **plan channel** `queries=99 same=99 changed=0` vs the
ON-arm baseline; pgbench smoke via the commit hook; `make ralph-state-guard`.

In-flight: none.
