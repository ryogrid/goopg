# 02 — Open problems

*Everything still unfixed as of 2026-09-19/20. Verified-fixed work is excluded
by construction (the M0143 ledger and P0-E7 re-measurement discharged that
class). Items are ordered by how much of the remaining gap they block.*

---

## Part A — Summary

### The strategic blockers

| # | problem | what it blocks | status |
|---|---|---|---|
| **S1** | **join-order is unmoved.** ~14 TPC-H / ~88 TPC-DS queries blocked; the candidate half is done, the costing half ended every pricing round blocked. Exact-tie elections (bit-exact, 2e-12) mean tie-break order is the decision for a whole class. | largest category, both corpora | open; M0142-0005 residual + frozen chain |
| **S2** | **Parallelism barely moved.** TPC-DS ~85/99 still diverge; Parallel Append machinery complete but 0/99 wins; TPC-H parallel-mode match is 2/22 (M0137-0017), 3/22 on P0-E7's re-measure. | second-largest category | machinery done; input-path divergence dominates |
| **S3** | **AGGSPLIT executor programme (S3–S6) is gated and unpriced.** Partial-mode row emission, `aggRuntime` serialize/deserialize, GatherMerge-fed Finalize — the real fix for 51 AGGSPLIT-touched TPC-DS queries. Its entry gate (a measured post-fix residual) is still open. | ~51 TPC-DS queries' agg-strategy | open, entry gate unmet |
| **S4** | **Semi/anti DP chain is owner-FROZEN.** `admitSemiAnti` is live; reachability ~0; the real unblock (IN-unnesting `.SJInfo` producer) is unfiled; Q78 sits at the deliberate `outer-over-derived` firewall. Owner reopen conditions exist and P0-E7's evidence now satisfies the factual half. | TPC-DS EXISTS/IN family {Q10, Q16, Q35, Q69, Q94} + Q78 | frozen pending owner |
| **S5** | **TPC-DS SF1 reads match=1/99** — below the SF0.25 floor of 2; Q9 diverges `[scan-type]` at SF1 (pre-existing, P0-H12). The SF1 corpus is measured once, not continuously. | the real-scale target | measured once; no cadence |
| **S6** | **TPC-H `:65433` still lacks the canonical 8 FKs** (script fixed, reload is owner-gated) and its `lineitem` row count diverges from both PG and canonical dbgen. The corpus the gates run against is not the corpus the schema now describes. | Q9-class FK-driven plans; measurement trust | owner reload pending |

### Instrument and measurement debts

| # | problem | effect | owner task |
|---|---|---|---|
| **I1** | `plan-gate` re-staled to **14/22** four days after the 9/15 re-pin; the instrument diffs the *live* binary (built 03:11) which now lags HEAD by plan-affecting commits | baseline pin is a decaying asset | M0137-0022 (open) |
| **I2** | Shared `:65436`/`:65437` clusters carry wall-clock-seeded stats; the "525→540" headline was measured pre-seed-pin | a recorded regression headline may be partly noise | M0137-0021 (open) |
| **I3** | 16 parallel-mode TPC-H `parallelism` divergences never triaged into plan-selection vs Gather-placement | the four serial-MATCH/parallel-DIFFER queries isolate a parallelism-only cause nobody has classified | M0137-0019 (open) |
| **I4** | PG-side oracle oscillation: Q2/Q75 flip plans between captures on tied-cost margins | ±1 verdict noise on those queries, permanently | recorded; not fixable at our layer |
| **I5** | Deferral ledger holds **~2,100 open rows**; the M0119 backlog-consumption milestone predates this era | institutional memory hazard — the INDEX files cover rounds, not the ledger | open, needs a triage cadence |

### Open task inventory (what remains selectable/blocked)

| milestone | open | highlights |
|---|---|---|
| M0137 | 3 | 0019 parallel-divergence triage; 0021 seed-pinned re-measure; 0022 plan-gate re-pin |
| M0139 | 0 | complete — but see S-facts: narrowing landed structurally inert |
| M0140 | 0 | complete — Parallel Append 0/99 wins stands as the record |
| M0141 | 13 | S2a-fix (open parent; children all landed), S2b-3b (WINDOW candidates), S2b-4 (SETOP rel-identity, sole witness Q49), S2b-8/-9 (Incremental-Sort offer points), S2b-15 (Gather rows stamp — impl staged uncommitted, needs gate re-run + ea triage + commit), S2b-16 (per-worker `rows=` display), S3–S6 (AGGSPLIT executor chain), S7 + S7-exec-d |
| M0142 | 1 open + 4 frozen | 0005b (streamed NL index-probe executor, then retire `indexProbeCostMultiplier` — B8 re-measurement already ran); frozen: 0008a-3, 0008c-1a, -3d, -4 |
| M0143 | 1 | 0007b bpchar blank-padding — **owner decision first** |
| P0 | 1 | P0-H11 stale-doc cleanup — not selectable until the owner's M0142-0008 decision is recorded |

### Engine-correctness residuals (real bugs, unscheduled)

| # | defect | evidence |
|---|---|---|
| C1 | `COPY … TO STDOUT` (table and query forms) fails `relation does not exist` inside a named DB — same per-DB scoping class as the known ANALYZE defect; blocks logical dump/restore of non-default databases | found 2026-09-19 during the TPC-H golden dump; workaround `psql --csv` |
| C2 | IsolationSuite residual: catalog-mirror drift (~23 "already exists", ~36 "dst extend" hits) after the WAL wedge fix — deferred at ledger | ledger 2026-09-19 IsolationSuite row |
| C3 | `wireRowMarkCtidColumns` doesn't descend Append/Gather/GatherMerge/SubqueryScan/Materialize — FOR UPDATE row locks skipped under wrappers | LockRows landing row |
| C4 | pgstat trigger store lacks abort reconciliation; `reportAnalyze` takes no live/dead args; `tuples_fetched`/index stats read 0 | IsolationStats row |
| C5 | FK-locking isolation items still open: `TestPort_IsolationFkContention`, `TestPort_IsolationFkDeadlock` | fix_plan nightly tail |
| C6 | 0–18 B `pg_node_tree` serialisation deltas on 9 views (PGColdStart residual) | ledger 2026-09-19 |

---

## Part B — Detail

### S1. Join-order — the unmoved centre

- Category state: TPC-H 14 at the last census (drifted 14→12→13→14 inside
  the ±3 band across the era), TPC-DS ~89→88. Net movement over 80 done /
  85 total M0142 tasks ≈ zero conversion.
- The candidate half is *done*: `inferTransitiveEqualities` unconditional,
  semi/anti plumbing landed, `SpecialJoinInfo`/`joinIsLegal` ported.
- The costing half is where every round ended blocked (R53/R68/R96/R98/R99
  carry-over pattern repeated inside M0142): margins are sub-percent and
  `setCheapest` takes the exact minimum, so **enumeration order + tie-break
  rule** is the decision at the margin. Two measured witnesses: Q9's L6
  bit-exact tie and Q45's 2e-12 flip. The open cross-engine question —
  *does goopg's DP enumeration order match PG's `join_search_one_level`
  when costs tie?* — is filed but not closed (M0142-0003c widened to take
  Q45 as a second witness).
- The 64%-real-gap counterexample matters: forcing PG's order into goopg on
  the Q9 witness costed 336,207 vs 204,932 — **not** a tie, so for that
  topology the divergence is upstream inputs (FK stats at the time), not
  tie-break. Both failure modes exist; the census proposed in 03 §2 separates
  them per query.

### S2. Parallelism — machinery complete, wins zero

- `GOOPG_GATHER_PATHS=all` is the shipped default (M0140-0003; `parallelism`
  86→85 at landing, TPC-H serial inert).
- Complete Parallel-Append pipeline (01 §B3) — `Parallel Append` appears in
  **0/99** goopg plans. Named residual blockers: Q76 needs M0142-0005a
  Memoize admission (landed, still no flip); Q14 needs K92's shared-hash-build
  executor model (ledgered "NOT cheap" — leader-prebuild is deliberate);
  merge/bitmap branch heads have zero occurrences in the PG corpus
  (completeness-only); the partial-bitmap producer is a separate gap
  (`e10-gathermerge-bitmap-untested-e2e`).
- The six-query Parallel-Append denominator is itself reference-dependent
  (committed fixture 5 vs SF0.25 capture 6) — recorded.
- TPC-H parallel-mode: 2/22 on M0137-0017's first capture, 3/22 on P0-E7's
  re-measure; `parallelism` divergent on 16/22 (artefacts stats-epoch-pinned,
  do not re-run). The four serial-MATCH/parallel-DIFFER queries (Q1, Q10,
  Q14, Q15a-VIEWBODY) are the cleanest parallelism-only specimens in the
  programme — M0137-0019.

### S3. AGGSPLIT — gated, unpriced, structurally the real fix

M0141-S1's census: of TPC-DS's 83 union-tagged queries (the union of the
agg/sort tag sets), **51 are AGGSPLIT-touched and 32 are fully serial in
PG's own plan**. The 51 need the
executor programme (S3 Partial-mode row emission → S4 `aggRuntime`
serialize/deserialize → S5 GatherMerge-fed Finalize → S6 wire+measure). S0's
scoping recon stands: goopg's zero-row-side-channel Partial is structurally
incompatible with `AggStrategySorted × {Partial,Final}`. The entry gate —
a measured post-fix parallel-shaped residual justifying the build — is still
open, so the whole chain sits correctly unscheduled. It is the largest single
*priced* unknown left in the programme.

### S4. Semi/anti — frozen with a known unblock

Owner freeze conditions (ledger `csq-R2`, 2026-09-17): reopen when (a) an A/B
names a query whose *only* residual diff is semi/anti placement, (b) the PG
mechanism is cited, (c) predicted movement is written, (d) gates green.
P0-E7's A/B supplied the factual half (23/24 identical, sole divergence =
Q9's both-arms timeout). The unfiled producer task — an IN-unnesting
`.SJInfo` producer reaching the searched arm — is the documented unblock;
explicit instruction on record: **do not** lift Q78's `outer-over-derived`
firewall as a shortcut.

### S5/S6. Corpus-scale and data-layer caveats

- **SF1 is the target scale and it is nearly unmeasured**: one capture ever
  (P0-E7), match=1/99, Q9's SF1 divergence proven pre-existing. Every
  category figure quoted in this programme is SF0.25. The noise band (±3) was
  calibrated on SF0.25 too.
- `:65433` is a *restored* cluster: data from the 9/15 pre-loss clone, FKs
  absent, binary built 03:11 (lags HEAD). A record-level golden
  (`tpch-golden-20260919/`, 21 tables, md5-manifested) now exists for
  forensic comparison, and `preloss-clone-20260915` remains the physical
  golden under permanent HOLD.
- `lineitem` divergence (L10) means TPC-H measurements compare goopg-plans-
  on-6.001M-rows against PG-plans-on-5.999M-rows — inside noise for most
  queries, but a real caveat for any margin-thin election.

### I-class detail

- **I1 plan-gate**: `Makefile:431-453` picks mtime-newest `plan_snapshots/`
  and diffs the live `:65433`. Re-staled to 14/22 diverged (8 MATCH) as of
  2026-09-19; M0137-0022 filed with the caveat that pinning HEAD's plans
  needs an owner-run rebuild+restart of `:65433`.
- **I5 ledger**: ~2,100 open rows is beyond per-task triage; the
  `analysis/deferral-ledger-summary-20260824/` digest is 26 days stale.
  A bulk-triage mechanism (or accepting the ledger as write-only) is an
  open methodological question — see 03 §6.
