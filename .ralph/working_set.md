Task: **M0127-P5.9-l-ii — channel BUILT and committed (`bf52391e`); the
MEASUREMENT is the remaining half and the item stays UNCHECKED.**

What landed (09 §3.12):
1. `internal/planner/joinsearchtrace.go` — one whole-block `DPTRACE` write per
   join problem under `GOOPG_PGSHAPED_DP_TRACE=1`: relid→name map, every
   offered `(outer relset, inner relset, phase)` triple with a `created` bit,
   every connectivity-gate refusal with its reason. `searchCtx.trace` is nil
   with the gate off; every call site is nil-safe.
2. `internal/estimateaudit/enumtrace.go` + `estimate-audit --enum-trace <log>`
   (arm script passes it when `DP_TRACE=1`) — adjudicates each spine-diff
   candidate `OFFERED` / `DECLINED` / `SIDE-NOT-BUILT` / `NOT-ENUMERATED` /
   `NO-TRACE`. Controls = goopg's OWN bushy pairings; a failing control prints
   `VERDICT: HARNESS FAULT` and voids the run.
3. Contract that makes the two channels one unit: the trace's pair key IS
   `SpineJoin.PairKey`'s string byte for byte, names follow `leafRel`'s
   alias-first rule (Q7's `n1`/`n2` must not collapse).

Key symbols: `searchTrace.{offer,decline,render,emit}`, `searchCtx.tracePhase`,
`makeJoinRel`, `ParseEnumTrace`, `EnumChecks`, `EnumTrace.Adjudicate`,
`RenderEnum`.

Findings: live smoke on a throwaway 4-rel cluster — chosen plan bushy, that
partition recorded at `phase=2` with `created=0` (a phase-1 pair reached the top
relset first, so "relset built" ≠ "partition offered"), alias preserved, an
unconnected partition → `SIDE-NOT-BUILT`. Evidence:
`analysis/leftdeep-joins/2026-08-06-p59lii-dptrace-{smoke.txt,README.md}`.

Next step: **run the arm** —
`DP_TRACE=1 PGSHAPED=1 scripts/tpch-estimate-audit-arm.sh 2026-08-0X-p59lii-enum-on --queries 7,8,20`
— then read the `=== ENUMERATION SUMMARY` section of
`analysis/leftdeep-joins/<label>.txt`. Q20's matched bushy pairing must come
back `OFFERED` (control) before any Q7/Q8 verdict is admissible. Then clause 6
passes (all OFFERED ⇒ cost/stats, §4 admits) or names a gap.

Gates run: `go build ./...`; `go vet` (planner, estimateaudit, cmd/estimate-audit);
targeted `go test` — 5 planner trace tests + 6 enumtrace tests + spine tests PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` (no FAIL);
pgbench smoke via the commit hook (10641 TPS, 0 failed); `bash -n` on the arm
script. No plan-shape change with the gate off, so no spotcheck/DS05 arm.

In-flight: none. The TPC-H arm was NOT started — the nightly CI batch held the
host (`pgrep -f ci/batch/run-nightly.sh` hit) and `tpch-estimate-audit-arm.sh`
refuses beside it; do not use `FORCE=1`, it contaminates both measurements.
Throwaway server on 5533 was stopped and `tmp/dptrace-data` removed.
