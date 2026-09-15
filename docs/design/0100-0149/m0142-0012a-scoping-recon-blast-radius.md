# M0142-0012a — scoping recon: how many corpus nodes hit the `Join{Lateral:true}` shape?

Status: accepted (recon closed 2026-09-15, no code change)

## Task

`.ralph/fix_plan.md`'s M0142-0012 (filed by M0142-0011) flagged "likely large
corpus-wide blast radius" for the `Join{Algo: JoinAlgoNestedLoop, Lateral:
true, Right: *IndexScan}` cardinality gap but did not measure it — the fix
was sized only by code inspection ("the common unmemoized NLI shape appears
throughout both corpora"). Per this milestone's own measure-first discipline
(and the working-set baton's suggestion), this task answers "how large" before
0012 is implemented, so 0012 can be scoped with a number instead of a guess.

## Method — count `estimateJoin` calls that hit the shape, per query, on both corpora

Reused the M0142-0010/M0142-0011 pattern: a temporary env-gated trace
(`GOOPG_M0142012_TRACE=1`) added to `estimateJoin`
(`internal/optimizer/cardinality.go`), firing once per call where
`j.Lateral && m0142012IsBoundIndexScan(j.Right) && len(joinEquiPairs(j)) == 0`
— exactly M0142-0011's finding's precondition for falling into the crude
`l*r*0.005`/`max(l,r)`-capped fallback. `git diff` on `cardinality.go` is
empty after this task (instrumentation added, measured, reverted — same
discipline as M0142-0010/M0142-0011).

Two private, throwaway servers (never the shared `:6543x`/`tmp/goopg-bench-bin`
lanes, per `goopg_bench_bin_shared_with_nightly_lane` / `worktree` isolation
convention):

- **TPC-H**: `bench/tpch/runtime_goopg/data` copied to
  `/tmp/goopg-m0142012-tpch-data` (2.0 GiB, so the real SF=1 HammerDB load's
  statistics carry over — no reload needed), served by a private instrumented
  binary (`GOOPG_CG_UNIT=m0142012-tpch`, port 5534, cgroup-capped per
  `scripts/goopg-test-run.sh`).
- **TPC-DS SF0.25**: started the (idle at loop start) `:65437` gate cluster's
  own git-tracked data dir with a private instrumented binary
  (`GOOPG_BIN=tmp/goopg-m0142012-bin`, same pattern M0142-0011 used).

Each corpus was run twice: once as a single full capture (`estimate-audit
-plan-only` for TPC-H, `scripts/capture-tpcds.sh` for TPC-DS) to get the
aggregate trace-line count from the server log, then per-query (`psql`/
`estimate-audit -queries <n>` one at a time, log-line delta between runs) to
attribute hits to individual queries. The counted number is **call-site hits
during planning**, not final-plan node count — the DP search calls
`EstimateRows` once per candidate path it costs, so a query with many
candidate nested-loop-index-probe paths inflates the raw total well past its
final plan's actual node count. Query-level incidence ("did this query's
search ever hit the shape") is the load-bearing number for scoping; the raw
totals are reported for completeness only.

## Findings

| corpus | queries with ≥1 hit | total call-site hits | which queries |
|---|---|---|---|
| TPC-H (22, Q15a view body excluded from direct EXPLAIN) | **6 / 21** | 224 | Q2 (10), Q7 (51), Q8 (94), Q9 (29), Q11 (21), Q21 (19) |
| TPC-DS SF0.25 (99) | **69 / 99** | 6347 | Q14 highest (1009), then Q80/Q49/Q64/Q88/Q23/Q60/Q56/Q33/Q61/Q66/Q38/Q87/Q24/Q72 all >90, remaining 55 queries lower but nonzero |

Confirms the "likely large corpus-wide blast radius" framing was correct and
gives it a number: **TPC-DS is dominated by this shape (70% of queries touch
it during planning)**; TPC-H is a minority (29%) but includes two of the
milestone's own named witnesses — **Q9** (the TPC-H `match=2` floor's other
half) and **Q21** (the M0077-era NLI/Path-B tuning target,
`m0077_q5_unlocked_4_slice`) — so even TPC-H's smaller share is
disproportionately load-bearing.

Cross-reference: M0142-0009/0010's own witnesses (Q25, Q34, Q68, Q69, Q73,
Q79, Q10, Q29) largely do **not** appear in the TPC-DS hit list above (Q73 is
absent; that gap is M0142-0005's Memoize/probe-multiplier interlock, a
different plan-shape-selection mechanism from this one, per M0142-0010's own
verdict) — the overlap with M0142-0005's population is small, so 0012 and
0005 are corroborating-but-distinct fixes, not the same fix under two names.

## What this recon does NOT establish

- **Not** how many of these hits survive into the FINAL chosen plan (a
  discarded candidate path that hit the shape during costing but lost the DP
  tournament contributes to the raw total with zero effect on the emitted
  plan). Sizing that precisely needs `EXPLAIN`-level post-hoc correlation,
  out of scope for this recon.
- **Not** a runtime/behaviour judgement — this task made no production code
  change, so there is no A/B to time. M0142-0012 itself must still run the
  full floor-measurement suite (TPC-H plan-parity, TPC-DS plan-parity,
  `make ea-ratchet`, SF0.25 regression sweep) before landing, exactly as its
  own filing already said — the toward-oracle hazard (AGENT.md's B6/B8/B10)
  applies to any accuracy-improving cardinality fix regardless of how it was
  scoped.

## Verification

- `git diff` on `internal/optimizer/cardinality.go` empty after revert;
  `go build ./internal/optimizer/...` clean.
- No stray servers: both private servers (`m0142012-tpch` cgroup scope,
  TPC-DS `sf025` scope under the private binary) stopped via their normal
  lifecycle commands (`goopg stop -D …`, `bench/tpcds/server.sh stop sf025`),
  confirmed via `ps aux` and `systemctl --user` `reset-failed` afterward. All
  scratch artefacts (`/tmp/goopg-m0142012-tpch-data`, `/tmp/m0142012-*`,
  `tmp/goopg-m0142012-bin`) removed.
- `go test ./internal/optimizer/...` PASS (unchanged from HEAD — no
  production diff this task).
