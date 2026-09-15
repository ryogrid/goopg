# M0142-0016a — scoping recon: how much of M0142-0016's blast radius is a real defect vs a no-op?

Status: accepted (recon closed 2026-09-15, no code change)

## Task

`.ralph/fix_plan.md`'s M0142-0016 (filed by M0142-0013's Q95 instrumentation)
found that `estimateLateralIndexJoin`/`estimateNLIndexJoin`'s plain-INNER
branches (`cardinality.go:382-396`/`:241-267`) return the outer row count
completely unconditionally, never consulting a residual-selectivity term the
way their own SEMI/ANTI arms three lines below already do. The task's own
filing sizes it "as a blast-radius measurement first (the M0142-0012a
precedent) before landing", because a cardinality change on this shape can
flip DP-search ties elsewhere in the corpus (the exact mechanism
M0142-0003c/M0142-0015 are independently investigating for Q9/Q45's level-6
ties). This recon answers "how large" and, along the way, clarifies exactly
which mechanism the eventual fix must call.

## What the fix actually needs (found while scoping, not implemented)

`joinResidualSelectivity(j)` — the term the filing suggested reusing — turns
out **not** to be the right primitive for this shape. Its inner loop
(`cardinality.go:1316-1345`) skips every clause where `exprSide(c, leftWidth)
!= sideMixed` (`internal/optimizer/planner.go:6795`): a clause referencing
only right-side columns is classified `sideRight`, not `sideMixed`, and is
never priced. Q95's witness filter (`ca_state = 'VA'`) lives entirely on the
inner/right relation, so `joinResidualSelectivity` would silently skip it too
— even the SEMI/ANTI arms that already call it do not price this class of
single-relation filter today (untested consequence, out of scope here).

The filter is instead carried on the bound probe node itself:
`IndexScan.Cond`/`IndexOnlyScan.Cond` (`plan.go:836`/`:1022`) is exactly the
"leftover, evaluated-once-per-probe" residual the NLI arm's own comment
describes — set only on a parameterized (NLI) index path, `nil` on a plain
scan (whose `*Filter` wrapper is rebuilt above it the ordinary way). Neither
`nliSemiMatchFraction` nor `lateralNLIMatchFraction` reads it; both compute a
pure equi-join selectivity from ndistinct statistics and stop. **The correct
fix target for M0142-0016 is `Cond`'s own selectivity, applied in addition to
whatever match-fraction/residual term already exists** — not a blind call to
`joinResidualSelectivity`, which cannot see this clause at all. Recorded here
so M0142-0016 does not have to re-derive it from scratch.

## Method — count real-vs-no-op hits per query, both corpora

Reused the M0142-0012a pattern: a temporary env-gated trace
(`GOOPG_M0142016_TRACE=1`) added to both plain-INNER branches
(`internal/optimizer/cardinality.go`), firing on every hit and classifying it
`hasCond=true` (the probe node is an `*IndexScan`/`*IndexOnlyScan` with a
non-nil `Cond` — a case where the current unconditional `return l` is
provably wrong) or `hasCond=false` (no such filter — the current code is
already correct for that hit, so fixing it would be a no-op there).
`git diff` on `cardinality.go` is empty after this task (added, measured,
reverted).

Two private, throwaway servers (never the shared `:6543x`/`tmp/goopg-bench-bin`
lanes):

- **TPC-H**: `bench/tpch/runtime_goopg/data` copied to
  `/tmp/goopg-m0142016-tpch-data` (SF=1 HammerDB load, no reload needed),
  served by a private instrumented binary (`GOOPG_CG_UNIT=m0142016-tpch`,
  port 5534).
- **TPC-DS SF0.25**: the (idle at loop start) `:65437` gate cluster's own
  git-tracked data dir (`bench/tpcds/runtime_goopg/data-sf025`), served by a
  private instrumented binary (`GOOPG_CG_UNIT=m0142016-tpcds`, same port
  65437 since the shared cluster was down).

Each corpus was run twice: once as a single aggregate `-plan-only` capture
(`cmd/estimate-audit` for TPC-H, `scripts/capture-tpcds.sh` for TPC-DS) for
the total trace-line count, then per-query (one `EXPLAIN` at a time, log-line
delta between runs) to attribute hits. As in M0142-0012a, the counted number
is call-site hits during DP-search costing (a query with many candidate
index-probe paths inflates the raw total past its final plan's node count),
not final-plan node count; query-level incidence of `hasCond=true` is the
load-bearing number.

## Findings

| corpus | queries with ≥1 hit | queries with ≥1 `hasCond=true` hit (real defect) | total hits | `hasCond=true` hits |
|---|---|---|---|---|
| TPC-H (21, Q15a view body excluded) | 9/21 (Q2, Q5, Q7, Q8, Q9, Q11, Q17, Q20, Q21) | **3/21: Q7 (25), Q8 (33), Q21 (19)** | 499 | 77 (all `kind=lateral`; `kind=nli` never carries `Cond` in this corpus) |
| TPC-DS SF0.25 (99) | 88/99 | **55/99** — Q77 (427) and Q49 (404) highest, then Q66/Q88/Q80/Q61/Q56/Q60/Q33/Q90 all >80, remaining 45 queries lower but nonzero | 9491 | 3633 (2700 `lateral` + 933 `nli`) |

TPC-DS's real-defect population (55/99, 38% of all hits) is far larger than
TPC-H's (3/21) — consistent with M0142-0012a's finding that TPC-DS is
dominated by the `Join{Lateral:true}`/NLI-index shape generally. **Q95
appears with 43 `hasCond=true` hits**, cross-validating M0142-0013's
direct-instrumentation finding independently via this recon's own (different)
trace. **Q9 has 90 total hits, all `hasCond=false`** — consistent with
M0142-0013's separate verdict that Q9's own estimation error is PG-formula-
identical, not a defect this mechanism causes: this recon shows Q9 hits the
shape often but the fix would be a no-op there every time.

TPC-H's three real-defect queries (Q7, Q8, Q21) are all named elsewhere in
this milestone: Q21 is the M0077-era NLI/Path-B tuning witness
(`m0077_q5_unlocked_4_slice`), and Q7/Q8 are two of the milestone's own
bushy-partition witnesses (09 §3.11/§3.13). A fix here has a real, if narrow,
chance to move TPC-H's plan set — the exact risk M0142-0016's own filing
flagged (DP-search-tie flips), now with named candidates to watch instead of
"the corpus at large".

## What this recon does NOT establish

- Not how many `hasCond=true` hits survive into the FINAL chosen plan (same
  scope boundary M0142-0012a declined).
- Not a runtime/behaviour judgement — no production code changed, so there
  is no A/B to time. M0142-0016 itself must still run the full
  floor-measurement suite (TPC-H/TPC-DS plan-parity, `make ea-ratchet`,
  SF0.25 sweep) before landing, per its own filing and the toward-oracle
  hazard (AGENT.md's B6/B8/B10).
- Not a fix for the single-relation-residual gap in `joinResidualSelectivity`
  the SEMI/ANTI arms already have (noted above as a separate, untested,
  out-of-scope observation — not filed as its own task since it was not
  confirmed to manifest on any corpus witness this loop).

## Verification

- `git diff` on `internal/optimizer/cardinality.go` empty after revert;
  `go build ./internal/optimizer/...` clean.
- No stray servers: both private servers (`m0142016-tpch`/`m0142016-tpcds`
  cgroup scopes) stopped via `goopg stop -D …`, confirmed via `ps aux` after;
  `systemctl --user reset-failed` run on both scope names. All scratch
  artefacts (`/tmp/goopg-m0142016-tpch-data`, `/tmp/goopg-m0142016-bin`,
  `/tmp/m0142016-out`, per-query scratch files) removed.
- `go test ./internal/optimizer/...` PASS (unchanged from HEAD — no
  production diff this task).
