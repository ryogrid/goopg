# 03 — Forward plan

*How the programme should work from here. The milestone era fixed the
instruments and proved the two biggest levers are election-fidelity and
cost-input timing; it also proved that building PG-spec mechanisms one at a
time does not convert categories into matches. This plan changes what gets
measured next, then what gets built.*

---

## Part A — Summary

### Keep — the parts of the current method that earned their place

- **The harness** (`AGENT.md` §Plan-parity): gate stamps bound to the staged
  tree hash, `CATEGORIES-EXCL-MATCH:` / `PARITY: N/A` commit discipline,
  stats-epoch declarations, floors as non-regression guards. All verified
  working this era — the floors held across ~180 tasks.
- **Recon-first decomposition** with `Kind:`/`Parent:`/`Movement:` fields and
  the S4 lineage budget — the mechanism that finally contained inert-plumbing
  chains.
- **"Instrument the term, never infer from the sum"** — extended by L7 to the
  diff tooling itself (the `parity.py` scorer fix was worth 25 ea-ratchet
  findings).
- **Adversarial review and pre-registered predictions** — the failures that
  were honestly scored (M0138's Q9 prediction) produced the era's most
  valuable finding.

### Change — what the era demonstrated is inefficient

| # | current shape | measured cost | replacement |
|---|---|---|---|
| 1 | Nine headline categories tag each query 4–7 ways, non-exclusively | "which fix unblocks the most queries" is not answerable from the metric; campaigns get chosen by judgment | **First-divergence census** (§1): one mutually-exclusive attribution per query |
| 2 | Every divergent node treated alike | thin-margin tie-breaks and thick-margin structural gaps get the same treatment and the same effort | **Cost-margin census** (§2): price PG's winning shape inside goopg's model at the divergence point |
| 3 | Attribution ends at "pricing" (R53/R68/R96/R98 pattern, repeated inside M0142) | the biggest category is the least-measured | **Instrumented PG 18.3** (§3): patched trace-GUC build dumping per-rel candidate lists and `add_path` verdicts (`OPTIMIZER_DEBUG` alone yields survivors-only) |
| 4 | Mechanism-across-corpus campaigns | three complete mechanisms, zero corpus wins (L5) | **Vertical-slice campaigns** (§4): drive one representative query to MATCH end-to-end |
| 5 | Per-task ledger triage only | ~2,100 open ledger rows; staleness compounds | bulk-triage cadence (§6) |

### The sequenced programme

| phase | content | output |
|---|---|---|
| **A — measure better** (days) | Build the two census tools on **existing captures**; build the instrumented PG on a private clone | ranked first-divergence table; per-node margin table; PG candidate-trace pipeline |
| **B — decide with data** (days) | The census output names the top blocker cluster; owner confirms scope; frozen items stay frozen until the owner rules | a short, ranked target list replacing milestone-scale guessing |
| **C — vertical slices** (weeks) | One representative query per top cluster, driven to MATCH; each layer it surfaces becomes a task | match conversion, or a *measured* statement of the residue |
| **Continuous** | M0143-class correctness carry-overs; owner-gated items (FK reload, bpchar, semi/anti unfreeze) proceed when unblocked | as before |

---

## Part B — Detail

### 1. First-divergence census — replace the headline taxonomy for targeting

**Problem.** `pg-plan-parity-diff.py` tags a divergent query with 4–7 of nine
categories, simultaneously. The counts answer "how many queries are touched by
this class" — they cannot answer "what is the *first* thing wrong in this
plan", because a query tagged `join-order` may diverge on a `scan-type` three
levels before the join order is even decided. All downstream targeting has
inherited that blur.

**Instrument.** Extend the differ (or add a sibling script) to align the two
normalised plan trees node-by-node — both are already parsed — and record, per
query, the **first node pair (in PG's plan-order, top-down) where the trees
diverge**: `(parent kind, PG child kind, goopg child kind, plan depth)`. A
subtree that diverges at node 3 cannot meaningfully be compared at node 5, so
one entry per query — mutually exclusive, summable, actionable.

**Output.** A frequency table over the corpus. Example shape (illustrative
only — the census does not exist yet):

```
first-divergence                          TPC-H  TPC-DS
inner join method at depth≥3              ...    ...
Gather presence at root                   ...    ...
scan-type at leaf (index vs seq)          ...    ...
```

**Why this is higher-resolution than categories.** A category says "this query
is tagged join-order"; a first-divergence says "its first wrong node is the
depth-2 inner of `lineitem⋈orders`, where goopg picks Nested Loop and PG picks
Hash Join". The former ranks mechanism classes; the latter ranks *decision
points* — which is what a fix actually targets.

**Cost.** Both trees already exist in committed captures; no new measurement
is needed to build the table — only the alignment code.

### 2. Cost-margin census — split "almost chose it" from "cannot reach it"

**Problem.** "Priced-and-lost" covers two different diseases: the winner
losing by 0.001% (an election/tie-break question — enumeration order, fuzz
band, `COSTS_EQUAL` ordering) and the winner being unexpressible or pricier by
2× (a structural gap — missing candidate, wrong input rows/width). M0142 spent
dozens of tasks discovering this distinction one query at a time.

**Instrument.** For each first-divergence node from §1, force PG's shape in
goopg and read the margin. Two existing mechanisms make this cheap:
`M0142-0016c`'s PG-forced-plan comparator already demonstrated forcing PG's
own plan shape, and goopg's planner flags (`GOOPG_*` admission arms) can
exclude the goopg winner. The margin goes into the census as a column:

| margin class | meaning | fix class |
|---|---|---|
| < 1% (inside fuzz) | election-order / tie-break | comparePaths-level work — bounded, already proven to move categories (S2b-13) |
| 1–20% | input divergence (rows/width/cost-term) | estimator or narrowing work — attribute the input first |
| > 20% or unexpressible | missing mechanism / structural | the mechanism is genuinely absent — fund it knowing the size |

**Why now.** Until this era the programme lacked both a trustworthy corpus and
the election-order fix; with `COSTS_EQUAL` restored and stats PG-faithful, a
margin measured today actually attributes to the residual cause instead of to
instrument noise.

### 3. Instrumented PG 18.3 — end the "attribution ends at pricing" stall

**Problem.** The most repeated failure of both eras: a hypothesis about why PG
chose plan X cannot be checked because PG's internal reasoning is opaque.
Every pricing round (R53, R68, R96, R98, R99, and inside M0142) ended at the
same wall — "we cannot observe what PG considered." R98 ended UNOBSERVABLE;
R99's only oracle route crashed PG.

**Instrument.** Build an **instrumented copy of PG 18.3**, run against a
private clone of the corpus on a `55xx` port (never the `:65432`/`:65438`
references — R1 applies to the instrumented build's data sources too):

- **`OPTIMIZER_DEBUG` build** — cheap but *limited*: grep of the bundled
  oracle shows only `allpaths.c` (`pprint(rel)` calls placed **after**
  `set_cheapest`) and a canonicalized-qual dump in `planner.c`. A rebuild
  with the macro defined yields per-rel **survivor** pathlists — rejected
  candidates are already evicted, and `add_path` prints nothing. Useful for
  "what won and what survived", not for the per-candidate verdicts the
  census needs.
- **Source-patched trace GUC** — the version that answers the real
  question. A small patch (built from a scratch checkout — the bundled
  `./postgres/` tree stays read-only) adding e.g. `debug_plan_candidates =
  on` that emits, per `RelOptInfo`: every pathlist entry at `add_path` time
  (node type, startup/total cost, rows, pathkeys, param_info,
  required-outer rels), the verdict for each rejected candidate (which
  comparator won), and the `set_cheapest` winner. This is the per-rel
  artefact goopg's `GOOPG_PGSHAPED_DP_TRACE` can be diffed against.
- **`-finstrument-functions` call-graph build** — answers a different
  question: not "what was considered" but "**which route through the planner
  code did this query take**". For a specific divergent query, the trace
  shows whether PG's plan came through `make_one_rel`'s DP search,
  `join_search_one_level` ordering, a `create_unique_path`, or a degenerate
  path — so the corresponding goopg code (and *only* that code) is the audit
  surface. This converts "read the planner" from a 200k-line problem into a
  named call list.

**Deliverable.** A capture pair — PG instrumented trace + goopg DP trace — for
each first-divergence cluster head, diffed mechanically: *which candidate PG
had that we never generated* (candidate gap) vs *which candidate we priced
differently* (costing gap) vs *which election went the other way at equal
cost* (tie-break gap). Today all three are called "join-order".

### 4. Vertical-slice campaigns — replace mechanism-across-corpus builds

**Problem.** M0140's record is unambiguous: six slices of verified-correct
Parallel-Append machinery, `PLAN-SHAPE 99/99 identical` at every slice. The
mechanism is complete and never wins, because its *inputs* diverge upstream.
The same shape repeated for Incremental Sort (0/99) and all absorption arms
(byte-identical). Building the Nth mechanism before its input paths match
PG's is measured-out as zero-yield.

**Replacement.** Pick the **top first-divergence cluster** from §1's table;
take **one** representative query; drive it to MATCH end-to-end, fixing or
filing each layer the slice surfaces (admission → candidate → cost input →
election → executor existence). The slice ends when the query matches or when
the residue is a named, measured, unfunded capability. Then — and only then —
generalise to the cluster.

This inverts the era's default (breadth-first per mechanism) into
depth-first per query. The evidence for depth-first: the era's only match
conversions came from exactly this shape (S2a-fix1 chased one width input to
the election; 0005a chased one driver's admission).

### 5. Measurement-hygiene upgrades

- **Decide the TPC-H protocol question.** Serial-mode TPC-H reads 8/22;
  parallel-mode reads 2/22 (M0137-0017) / 3/22 (P0-E7). The programme currently headlines
  the serial number. M0137-0019 (triage the 16 parallel divergences) is the
  entry task; its output should decide whether parallel-mode becomes the
  headline.
- **SF1 cadence for TPC-DS.** One SF1 capture exists ever (match=1/99,
  P0-E7/H12). SF0.25 is a proxy — the goal's corpus is SF1. Add a
  per-milestone-boundary SF1 capture, or at minimum one before the next
  stocktake.
- **`plan-gate` cadence.** It diffs the live `:65433` (binary built 03:11,
  now behind HEAD). Either re-pin on a schedule (M0137-0022 filed) or reframe
  it to diff a fresh private-clone build — as designed today it can never see
  staged-code regressions, only live-vs-baseline drift.
- **Golden-record discipline.** `tpch-golden-20260919/` is now the
  record-level reference for `:65433`; if the cluster is ever reloaded (e.g.
  for the FK set), re-dump first.

### 6. Ledger debt — a cadence, not a heroic pass

~2,100 open ledger rows cannot be triaged row-by-row inside tasks. Proposal:
a periodic **bulk-triage pass** owned by a milestone (M0119's successor) that
(1) auto-detects rows whose referenced code/tests no longer exist, (2) folds
same-mechanism rows into cluster rows, (3) escalates only the survivors.
Until then the ledger should be treated as an append-only archaeology layer
and *not* as a work queue — the INDEX files already carry the query/mechanism
view for the round corpus.

### 7. Owner decisions that block named work

These are recorded for the owner's convenience — the loop escalates rather
than acts on them:

| decision | what it unblocks |
|---|---|
| Reload `:65433` with the canonical 8-FK schema (script already landed) | Q9-class FK-driven plans; honest TPC-H measurement |
| Semi/anti chain unfreeze (conditions in `csq-R2`; P0-E7 evidence now satisfies the factual half) | TPC-DS EXISTS/IN family {Q10, Q16, Q35, Q69, Q94} + Q78 |
| M0143-0007b bpchar blank-padding (reverses a load-bearing convention) | K41 `relpages` floor under TPC-DS parallelism |
| `plan-gate` baseline rebuild of `:65433` binary from HEAD (owner-only restart) | M0137-0022's "pin HEAD, not the stale binary" variant |

### 8. What success looks like under this plan

| horizon | signal |
|---|---|
| Phase A done | A ranked first-divergence table exists for both corpora; every open task can cite its cluster; the instrumented-PG trace answers "what did PG consider" for one query end-to-end |
| Phase B done | The next campaign is chosen from the table, not by judgment; each target carries its margin class |
| Phase C in progress | One query per top cluster reaches MATCH or a named measured residue; match count resumes moving — or the residual is finally priced honestly |
| Continuous | Ledger stops growing faster than it is triaged; correctness dividends keep landing |
