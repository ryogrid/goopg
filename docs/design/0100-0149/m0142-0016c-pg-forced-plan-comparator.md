# M0142-0016c — does real PG qerr-match the M0142-0016b Q33/Q54/Q56 shape?

Status: accepted (recon closed 2026-09-18, no production code changed)

## Task

M0142-0016b landed a fix that made goopg apply a probe's residual selectivity
in plain-INNER NLI/Lateral-index joins. `make ea-ratchet` went from 95 to 110
findings: 2 FIXED (the task's own motivating witnesses), 17 NEW across
`Q33`/`Q54`/`Q56` — all tagged `UNMATCHED-IN-PG` (`pg_est=None`,
`pg_qerr=None`) by the scorer, read at the time as "PG does not reach this
dimension-table-behind-a-parameterized-probe shape for these three queries,
so the PG-relative bar has no floor to check against; architecturally this is
the same class of independence-assumption estimation weakness PG's own
formula would produce in an analogous shape" — an inference, not a
measurement. M0142-0016c was filed to replace the inference with a real
PG-side comparator: force (or find) an equivalent plan shape on real
PostgreSQL 18.3 and read its own qerr for the same node.

## Method

Real PG 18.3 reference (`:65438`, db `tpcds025`, the same SF0.25 dataset the
sf025 fast-gate and `make ea-ratchet`'s PG comparator use) is read-only for
the loop (no DDL/DML/ANALYZE) — this task only issues `EXPLAIN (ANALYZE)` /
session-scoped `SET` on the exact query text from
`bench/tpcds/runtime_goopg/tpcds-data/queries/query{33,54,56}.sql`, which is
compliant.

- **Q54**: ran the full query's `EXPLAIN (ANALYZE, VERBOSE, BUFFERS)`
  unforced (no knobs). PG's own default, cost-optimal plan for the
  `my_customers` CTE already contains `Index Scan using date_dim_pkey …
  Index Cond: (d_date_sk = catalog_sales.cs_sold_date_sk) Filter: ((d_moy =
  1) AND (d_year = 1999))` — the identical parameterized-probe-with-residual
  shape M0142-0016b's goopg witness traced. No forcing needed at all.
- **Q33**: the isolated single-branch `ss` CTE was tried first with
  `enable_hashjoin=off`/`enable_mergejoin=off` (the wrong instrument — see
  below), then the **full, unforced** query was run and turned out to
  already contain `Index Scan using customer_address_pkey … Index Cond:
  (ca_address_sk = ss_addr_sk) Filter: (ca_gmt_offset = -5)` for **all three**
  branches (`ss`, `cs`, `ws` — `customer_address`, `customer_address_1`,
  `customer_address_2`), at the exact cost PG's own `bench/tpcds/plans-pg/Q33.txt`
  capture already has (`cost=4601.47..20520.29` etc. — byte-identical),
  confirming this is PG's genuine, already-captured default choice, not an
  artifact of a knob.
- **Q56**: same check, full unforced query — three
  `Index Scan using customer_address_pkey[/_1/_2]` nodes present, matching
  `bench/tpcds/plans-pg/Q56.txt`.

**Correction made mid-task**: the isolated-branch-plus-knob-forcing approach
was abandoned once the full-query unforced EXPLAIN showed the shape was
already PG's real choice — forcing was never necessary and would have been
the wrong artifact to report (a knob-forced plan's cost is not what PG
"decided"; PG's own default is).

## Finding 1 — PG *does* qerr-match, closely, on all three queries

| query | node (real PG, unforced default) | PG est | PG actual | PG qerr | goopg est/actual/qerr (0016b, same shape) |
|---|---|---:|---:|---:|---|
| Q54 | `Index Scan using date_dim_pkey` residual `d_moy=1 AND d_year=1999` | 1 (clamped) | ~136 total (0.01×13607 loops) | ~136 | est=1, actual=121, qerr=121.0 |
| Q33 `ss` | `Gather` over the `customer_address`-probe Nested Loop | 101 | 2817 | 27.9 | est=99, actual=2817, qerr=28.5 |
| Q33 `cs` | ditto, `customer_address_1` | 55 | 1309 | 23.8 | est=53, actual=1134, qerr=21.4 |
| Q33 `ws` | ditto, `customer_address_2` (Gather Merge) | 28 | 757 | 27.0 | est=25, actual=757, qerr=30.3 |
| Q56 `ss` | ditto | 12 | 262 | 21.8 | est=118, actual=2804, qerr=23.8 |

Q33's three branches match goopg's own qerr to within ~15% on the *same*
node, using the *same* SF0.25 dataset PG's static `plans-pg/` capture was
taken from. Q56's absolute cardinalities differ by roughly 10x between the
two systems' node — a real divergence upstream of this node, out of this
task's scope — but the **qerr ratio** (how many times off the clamped
estimate is) lands in the same 20-24x band on both sides, the metric this
task is answering the question with.

**Answer to M0142-0016c's first question: yes, confirmed by direct
measurement, not by architectural analogy.** M0142-0016b's "not a blocker"
verdict stands, now on firmer footing — PG independently reaches the exact
same shape by default (not merely "PG would reach an analogous shape if
forced") and posts a comparably bad clamped-to-1 estimate on it.

## Finding 2 — no, the cost signal should not move the plan choice

Because PG's own planner, carrying the identical bad row estimate, still
picks this shape as ITS cost-optimal plan (it does not cost it away in favor
of an alternative), there is no PG-side signal implying goopg's cost model
should avoid it either. (The knob-forced Q33-`ss`-only experiment along the
way did show a *more expensive* forced total (24786.53) versus PG's own
unforced total (20520.29) for that single branch — but that number was never
PG's actual decision on the real, full query; it is dropped from the
conclusion, see the correction above.)

## Finding 3 — the real cause of "UNMATCHED-IN-PG": a scorer key-mismatch, not a missing PG-side node

Both PG-side captures used by `scripts/estimate-parity/parity.py`
(`bench/tpcds/plans-pg/Q33.txt`, `Q54.txt`, `Q56.txt`) **already contain**
these exact nodes at cost numbers byte-identical to a fresh live capture —
the PG comparator data was correct all along. The mismatch is in the
scorer's own node key.

`parity.py`'s `annotate_relsets` builds each node's match key from the set of
base relations in its subtree; a `CTE <label>` marker line
(`MARK`/`scope = strip_alias_suffix(label)`, `parity.py:202-220`) opens a
**scope** that gets prefixed onto every relation beneath it
(`base_relation(name, scope)`), so goopg's capture — which still prints a
`CTE ss` / `CTE cs` / `CTE ws` / `CTE my_customers` marker for these
single-reference CTEs — produces keys like `ss.customer_address`,
`ss.date_dim`, `ss.item`, `ss.store_sales` (exactly what the 0016b findings
JSON shows). PG's plan for the same query carries **no such marker at all**:
PG 18.3's planner structurally inlines a single-reference, non-recursive,
non-volatile CTE (`inline_cte`,
`postgres/src/backend/optimizer/plan/subselect.c`) into an ordinary
sub-select before join-order search runs, so its relation names are bare
(`customer_address`, `date_dim`, …, no scope prefix at all). The scorer's
tuple-of-sorted-relset key therefore never matches
(`{ss.customer_address, …}` vs `{customer_address, …}`), and every node
under a goopg `CTE` marker for one of these three queries is unconditionally
reported as `UNMATCHED-IN-PG`, regardless of whether PG's plan actually has
the corresponding node (it does, per Finding 1).

goopg has an existing, narrower analogue of PG's `inline_cte`:
`pushQualsThroughSingleRefCTEs`
(`internal/optimizer/cte_inline_pushdown.go`) pushes a QUALIFYING PREDICATE
through a single-reference CTE's boundary into its body, but — by its own
doc comment — does **not** remove the CTE Scan boundary itself the way PG's
`inline_cte` does; the CTE remains its own planning unit with a `CTE Scan`
node in the final plan, and join-order search cannot freely reorder its
relations against the rest of the query. That structural difference (not a
row-estimate defect) is what breaks the scorer's key, and is plausibly a
real, independent, larger planner-parity question in its own right (does the
narrower CTE boundary ever cost goopg a better join order PG's `inline_cte`
would find?) — flagged, not scoped, in the deferral ledger row below; no
task filed for it yet pending the bounded scorer fix's own measurement.

## Deferred / follow-up

- `.ralph/deferral_ledger.md` row appended (task-id `M0142-0016c`): the
  scorer key-mismatch (Finding 3) and the larger CTE-inlining-vs-qual-pushdown
  structural question it points at (goopg has no PG-`inline_cte` analogue,
  only `pushQualsThroughSingleRefCTEs`) are both unresolved.
- Follow-up task filed: `.ralph/fix_plan.md` **M0142-0016d** — fix
  `parity.py`'s relset key to strip (or otherwise normalize away) a
  goopg-only single-reference-CTE scope prefix before comparing against PG's
  un-scoped key, so these 17 findings score against PG's real qerr instead of
  `UNMATCHED-IN-PG`; almost certainly turns `make ea-ratchet` back to PASS
  without any planner code change (bounded, tooling-only, low risk — the
  right size for a single loop). The bigger CTE-inlining question is left
  for that task's own follow-up read, not filed as its own task yet (its
  scope is unmeasured).

## Verification

- No production code changed this task (`internal/...` untouched); no
  optimizer/executor gate re-run required for THIS task itself.
- `scripts/estimate-parity/parity.py`, `bench/tpcds/plans-pg/{Q33,Q54,Q56}.txt`
  read-only (not modified).
- Real-PG probes: `EXPLAIN (ANALYZE, VERBOSE[, BUFFERS])` / session-scoped
  `SET enable_hashjoin`/`SET enable_mergejoin` only, against `:65438`
  (`tpcds025`) — no DDL/DML/ANALYZE, compliant with the reference-cluster
  read-only rule.
- goopg-side comparator: private SF0.25 clone started via
  `GOOPG_BIN=tmp/m0142-0016c-bin bench/tpcds/server.sh start sf025` (data dir
  was already present, untracked, from a prior loop's sf025 gate run — not
  reset), `EXPLAIN` only (no ANALYZE — the DP-search shape check did not need
  execution numbers, only the chosen plan), stopped via
  `./tmp/m0142-0016c-bin stop -D bench/tpcds/runtime_goopg/data-sf025`
  (direct binary invocation, not `server.sh stop`, since the RALPH_LOOP guard
  refuses `server.sh stop/restart` for any TPC-DS lane — see
  `scripts/ref-clusters-ensure.sh` and `CLAUDE.md` "Running a server
  manually"); `tmp/m0142-0016c-bin` removed after use; `ps aux` confirmed no
  orphan process on port 65437 or matching the binary path.
