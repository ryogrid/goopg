# M0145\-0023 — Flow\-convergence instrument

Status: **LANDED report\-only 2026\-09\-22.**
Kind: impl
Parent: none
Movement: none — this is a secondary observability channel, not an S3 movement instrument.

## Purpose

The final plan\-parity score is deliberately strict: structural migration work can make the planning flow closer to the intended jointree path while leaving `MATCH` and `CATEGORIES\-EXCL\-MATCH` unchanged. This instrument makes that intermediate fact visible, but does not change the lineage guard, a gate result, or task movement.

## Design

The SF0.25 sweep already ends with a fresh EXPLAIN\-only plan capture. Its server is now started with `GOOPG\_NLI\_CENSUS=1` and `GOOPG\_PGSHAPED\_DP\_TRACE=1` only for that tail. The harness records the starting line of the shared server log and copies only the newly written range into a sibling `plans\-…\.flow\.log` file. This prevents prior sweeps' events from being accidentally recounted.

`scripts/flow\-convergence.py` consumes that isolated log. It requires at least one `SUBLINKCENSUS` event, reports the `pinned\-spine` and `jointree\-pullup` totals plus their ratio, and lists every `seam\-decline reason=…` bucket. It appends one tab\-separated row to `flow\-convergence.tsv` alongside the sweep artefacts. A missing sublink event is fatal to this report channel rather than silently producing a zero ratio; like the plan\-diff zero\-block guard, that prevents a vacuous observation.

The report runs after the value summary and remains non\-blocking. The existing plan pin retains its own semantics. A flow\-report failure is printed in the sweep report but never changes the correctness verdict.

## Existing producer contracts

`internal/optimizer/nlicensus.go:45\-89` reads `GOOPG\_NLI\_CENSUS` once at server startup and emits `SUBLINKCENSUS route=…` at the two planning routes. `internal/optimizer/joinsearchtrace.go:29\-49` and `joinsearchseam.go` use `GOOPG\_PGSHAPED\_DP\_TRACE` for the existing `seam\-decline` trace. The harness only aggregates these diagnostic streams; it changes neither election nor PostgreSQL\-visible behaviour, so there is no upstream planner algorithm to port or claim as PG\-faithful.

## Verification

`flow\-convergence.py \-\-self\-test` exercises both route counters, repeated decline buckets, and unrelated trace lines. `bash \-n scripts/tpcds\-sf025\-regression.sh` checks the tail wiring. A full SF0.25 sweep is the integration gate: it must retain the existing zero\-mismatch value summary while appending a non\-vacuous flow record.
