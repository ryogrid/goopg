# M0147 — Lever retirement

**Filed:** 2026-10-21 (owner decision).
**Status:** planned — banner item 10: M0122-0012 → **M0147** → the rest of M0122; **M0122-0012 (its inline
slices and any task with `Parent: M0122-0012`) outranks every M0147 task** until it yields (banner item 10).
**Basis:** the 2026-10-21 lever census and its review
(`/home/ryo/work/tmp/ef5e65e27094282ab28769901e05621c/0----------plan-party-progress-1021.md`), and the
census at `analysis/m0146/m0146-0014/routing-20261021p` (TPC-H 22/22, TPC-DS SF0.25 99/99, SF1 99/99).

## Why this milestone exists

The plan-parity work (M0137–M0146) left behind environment levers (`GOOPG_*`) that select between
alternative code paths. With plan parity complete, the alternatives are now liabilities: a default-OFF arm is
either a PG 18.3 port that was never promoted, or a goopg-private experiment, and a default-ON lever's OFF arm
is goopg-private legacy logic that no gate exercises. AGENT.md C5 already says a default-off arm resolves to
*promote* or *delete* — HOLD is not an outcome. M0147 resolves every one.

## Strategy

- **Phase 1 — goopg-private default-OFF levers (delete):** the S30.3/S32.x probes (PAGEIDENT, S321/S322 —
  keeping the pending-writer waits the NOWAIT switches could disable), SORT_PACKED (B1 NO-GO), HEAP_PREFETCH_DEPTH,
  SCALAR_UNNEST, HASH_OUTER_JOIN, FORCED_GC, the dead RELSIZE_FALLBACK read and stale lever comments, and
  DISABLE_NLI (the M0054-0006 rollback switch; setting it disables the nested-loop index join).
- **Phase 0 — default-OFF levers whose ON arm ports PG (promote, then delete the OFF arm):** PARTIAL_SORT_PATHS
  (first check the post-pass is still reached), PG_HASH_TUPLE_SPILL_COST (C3 acceptance arm first: memory),
  PG_MEMOIZE_ENTRY_BYTES_COST (complete the `get_expr_width` term first), NL_BARE_REEXEC (blocked on
  M0146-0010a). R3: a PG arm is never discarded without an owner `[!]` decision.
- **Phase 2 — default-ON levers' OFF arms (delete after rebasing their tests onto PG oracles):** a recon task
  inventories them and files one slice per lever.

Phase 1 runs first (lowest risk, establishes the procedure), then phase 0, then phase 2 — a deliberate
reordering of the review doc's §5, whose phase 0 needs per-lever preconditions (C3 memory arm, the Memoize
port, M0146-0010a).

## Slice protocol

One task per commit (M0147-0010's four probe switches share their files and retire together); shared helpers kept (build, then delete what the compiler reports unused); the
`internal/optimizer/flaglabels.go` row, if the lever has one, stays and becomes `retired(<task id>)` with `scripts/planner-flags.env`
regenerated in the same commit; inert proof per AGENT.md C6; gates: units, regress, isolation, tpch-spotcheck,
acceptance arm, sf025 PASS=99 same=99, tpcds-fireset fires=none, S9 census unchanged. A deletion that moves any
plan or value broke a default path — fix it, never re-pin. Ledger resolutions are new rows (R7).

**Instrument:** `scripts/lever-count.sh` — distinct `"GOOPG_*"` literals in non-test `internal/` and `cmd/` Go
code, excluding `flaglabels.go` and the keep list. 34 at filing. Closing form:
`Movement: yes — lever-count N→N-k (census 22/22·99/99·99/99 held)`.

## Keep (out of scope; excluded from lever-count)

`GOOPG_ANALYZE_SEED` and `GOOPG_ANALYZE_SEED_CHILD` (every capture/gate pins the seed), the diagnostic traces (`PGSHAPED_DP_TRACE`, `NLI_CENSUS`,
`GEQO_TRACE[_EVAL]`; estimate-audit and census tooling read them), `GOOPG_NLI_COSTGATE` and `GOOPG_NLI_COSTGATE_DEBUG` (owner decision C-20f
keeps the `legacy` hatch), `GOOPG_WAL_ALLOW_EARLY_END` (operator recovery), the test-harness switches
(`UPDATE_GOLDENS`, `SKIP_*`, `RUN_BLOCKED_*`, `REGRESS_*_DIR`, `TPCH_QUERIES`, `PPROF_ADDR`, `TEST_*`), profiling
(`*_PROFILE_RATE`, `LOG_STATEMENT`, `IO_TRACE`, `DEBUG_WFG`, `BTREE_PARSE_ERR_DUMP`, `SEGV_BACKTRACE_SO`) and the safety assertions
(`ASSERT_ROW_SHAPE`, `PDLSN_ASSERT`). PG GUC default differences (`work_mem`, `max_parallel_workers_per_gather`,
`enable_partitionwise_*`, …) are not levers and are out of scope.

## Tasks

| id | phase | lever(s) |
|---|---|---|
| M0147-0000 | — | tracking root (a channel, never selected) |
| M0147-0010 | 1 | PAGEIDENT_PROBE, S321_PROBE, S321_NOWAIT, S322_NOWAIT |
| M0147-0011 | 1 | SORT_PACKED |
| M0147-0012 | 1 | HEAP_PREFETCH_DEPTH |
| M0147-0013 | 1 | SCALAR_UNNEST |
| M0147-0014 | 1 | HASH_OUTER_JOIN |
| M0147-0015 | 1 | FORCED_GC |
| M0147-0016 | 1 | RELSIZE_FALLBACK (dead read, tpch-relsize-arm.sh) + stale comments + unused `envFloatDefault` |
| M0147-0017 | 1 | DISABLE_NLI |
| M0147-0020 | 0 | PARTIAL_SORT_PATHS |
| M0147-0021 | 0 | PG_HASH_TUPLE_SPILL_COST |
| M0147-0022 | 0 | PG_MEMOIZE_ENTRY_BYTES_COST |
| M0147-0023 | 0 | NL_BARE_REEXEC (`[!]` blocked-on M0146-0010a; closes as a pointer when 0010a lands) |
| M0147-0030 | 2 | recon: default-ON levers' OFF arms → files M0147-0031… |

## Acceptance

Every task `[x]`; lever-count 0, or only owner-`[!]`-retained levers remain (R3) — the instrument excludes the keep list; plan parity
unchanged on all three corpora and every values gate green at each step.
