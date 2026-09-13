# R125 SCOPE (rev 2) — goopg refuses `NOT VALID` foreign keys as planner evidence; PG does not. Fix the guard, not the corpus.

Rev 1 proposed adding the eight missing FKs to the goopg TPC-H bench
database. **Review BLOCKed it on four findings, all correct**, and the
findings relocate the work from the corpus to the engine. Rev 2 takes
the reviewer's step (a): the smallest, PG-cited, source-level defect on
the critical path.

## 1. What rev 1 got right, and what it got wrong

**Right — and independently reproduced by the reviewer:** goopg's FK
path is live end to end. DDL → `catalog.Table.ForeignKeys` →
`keysCovering` (`joinrelsize.go:657`) → `superkeyJoinSelectivity` →
joinrel size. On a query shaped like Q9's contested join, adding the FK
alone moved the estimate **`rows=5` → `rows=1200`, exactly ground
truth**. The estimator works; it is starved of evidence.

**The measured premise also holds:** PG `tpch` has 8 FKs, goopg `tpch`
has 0; `lineitem(l_partkey,l_suppkey) → partsupp` is declared on PG with
`convalidated='t'`; goopg estimates Q9's contested join at **116**, PG
at **363,341**, ground truth **318,748**.

**Wrong, in four ways:**

1. **goopg does not persist FK constraints across a restart.**
   Demonstrated on a throwaway cluster: create FK → `pg_constraint`
   shows it → CHECKPOINT → stop → start → **0 rows**, data intact.
   `catalog.Table.ForeignKeys` (`catalog.go:641`) is the only store,
   `pg_constraint` is synthesised from it (`catalog.go:7248`), and
   startup has **no FK reload path**. Rev 1's gates 2 and 6 both cross a
   restart, so the cut was a no-op by measurement time — and "bake the
   DDL into the bench tooling" misdiagnosed it: the loss is per-restart,
   not per-rebuild.
2. **Validation is O(child × parent) with no index path**
   (`operators_ddl.go:9121` → `validateFKConstraintExistingRows` →
   `assertParentExists` → `scanRelForFKMatch`, a full heap scan).
   Measured **32.17 s for 40,000 × 8,000**. Extrapolated:
   `lineitem→partsupp` **~5.5 days**, `lineitem→orders` **~10 days**,
   ≈ **two weeks** for the eight. Rev 1 called this "may be slow" — five
   orders of magnitude off — while forbidding the only escape.
3. **Rev 1's `NOT VALID` prohibition was factually wrong about PG**
   (§2 — this is now the round).
4. **Rev 1's causal story is refuted.** goopg **accepted all eight FKs
   at load**: HammerDB's `CreateIndexes` runs PKs, then the 8 FKs
   (`sql(9)-(16)`), then the 8 `*_fkidx` indexes, erroring out on first
   failure — and goopg has all eight `*_fkidx` indexes. So the FKs
   succeeded and were lost at a later restart. The ANALYZE failure came
   afterwards and is unrelated. (At that epoch validation was also a
   no-op: existing-row validation landed later, in `0518b4a48`,
   2026-08-16.)

## 2. The defect this round fixes

PG's planner **does not consult `convalidated`**:

```c
/* skip constraints currently not enforced */
if (!cachedfk->conenforced)
    continue;
```
`get_relation_foreign_keys`, `plancat.c:642-644`. `RelationGetFKeyList`
(`relcache.c:4769-4776`) filters on `contype == CONSTRAINT_FOREIGN` and
carries `conenforced` only — `convalidated` never reaches
`ForeignKeyCacheInfo`. **So PG uses a `NOT VALID` FK for
`get_foreign_key_join_selectivity`.**

goopg refuses it, at two sites:

```go
if fk.NotValid || fk.NotEnforced || !columnsSubset(fk.Columns, cols) {
```
`joinrelsize.go:658` and `joinkeyproof.go:740`.

That is a straight PG-parity divergence in a function goopg otherwise
ports faithfully. It is also the **unblocking** fix: with `NOT VALID`
accepted, FK evidence becomes declarable in O(1) instead of O(child ×
parent), which is what makes hazard 2 survivable at all.

## 3. The cut — and the one real design question

Relax the guard to `fk.NotEnforced` only, matching `plancat.c:643`.
**But the two sites are not equivalent, and this is the question the
round must answer rather than assume:**

- `joinrelsize.go:658` feeds `superkeyEstimate.sel` — a **selectivity**,
  i.e. a cost estimate. A `NOT VALID` FK that the data violates makes it
  optimistic, exactly as it does in PG. **Safe to relax; PG does.**
- The same struct also carries `boundProven` / `rowsBound`, documented
  as a *"STRUCTURAL upper bound"*, and `joinkeyproof.go:740` is a
  **proof** site. If any consumer treats that bound as a guarantee
  rather than an estimate — e.g. to elide work or to justify a
  uniqueness assumption — then honouring an unvalidated FK there is
  unsound in a way it is not in PG, because PG derives no such bound
  from `fkey_list`.

**Required before implementing:** enumerate every consumer of
`boundProven`/`rowsBound` and of `joinkeyproof`'s FK arm, and decide per
site. The likely correct shape is *relax the selectivity site, keep the
guard on any hard-bound/proof site*, with the asymmetry documented at
both. Relaxing both without checking would be exactly the class of error
this series keeps catching.

## 4. Predictions

| # | Claim | Verdict on miss |
|---|---|---|
| P0 | No behaviour change when no `NOT VALID` FK exists — both corpora bit-identical, since neither declares any FK at all today | any movement ⇒ the guard was doing something else ⇒ STOP |
| P1 | With a `NOT VALID` FK declared on a synthetic Q9-shaped fixture, the join estimate corrects to the FK-driven value, matching what a validated FK gives | unchanged ⇒ the relaxation did not reach the estimator |
| P2 | Every `boundProven`/`rowsBound` consumer is enumerated in the REPORT, with a per-site decision and its justification | not enumerated ⇒ the round is not done |
| P3 | Suites green; no values gate needed (no corpus change), but say so | — |

**No prediction about Q9 or about parity counts.** This round declares
no FK on the bench corpus and therefore cannot move a plan there. Q9
remains blocked behind FK **persistence** (finding 1), which is a
separate round.

## 5. What this does NOT do, and the road behind it

Rev 1's ambition is deferred to a dependency chain the reviewer laid out
and I accept:

- **(a) this round** — consume `NOT VALID` FKs as PG does.
- **(b) persist FK constraints across restart.** The blocker for
  everything else. Mechanism per the existing catalog-DDL durability
  split (pg_class heap-append vs goopg-private WAL + startup replay);
  `pg_constraint` being *synthesised* from `catalog.Table.ForeignKeys`
  means the reload must repopulate that field, not the view.
- **(c) give FK validation an index path** via the parent's unique
  index, as PG's `validateForeignKeyConstraint` does — turning ~2 weeks
  into minutes.
- **(d) then** reload/redeclare the corpus and measure Q9.

Each of (a)–(c) is a real PG-parity defect that stands on its own merits
independent of Q9.

## 6. Hazards and carried findings

- **Re-baseline** (the one part of rev 1 §7 that survives): every
  capture in this workstream predates FK evidence. When (d) lands,
  prior numbers are not comparable across that boundary.
- **Deferrability**: HammerDB declares `LINEITEM_ORDER_FK` DEFERRABLE
  and the other seven NOT DEFERRABLE. Neither planner reads it, but a
  future "catalogs match" gate must not compare only
  `(conrelid, confrelid, conkey)` and call it byte-comparable — and
  should first verify goopg populates `conkey`/`confkey` at all.
- **Neither `tpch` database is clean**: goopg's public schema also holds
  `agg_data, lrs_acct, mj_*, zz_*, tmp1`; PG's holds `cf4,
  ralph_bit_test*, zz_*`. No FKs on any of them, but scope any
  like-for-like claim to the eight benchmark tables.
- **TODO doc drift** (worth one line): the TODO head's values gate says
  "TPC-DS SF0.5 sweep PASS=95" while CLAUDE.md documents the SF0.25 gate
  and every round since R94 reports PASS=96.
- Rev 1's P1 arithmetic, for whoever reaches (d): with the FK firing,
  goopg's estimate is computable —
  `40,132 × 6,001,255 / 800,000 ≈ 301,050`, **not** PG's 363,341 (PG's
  `partsupp ⋈ part` is 48,484 where goopg's is 40,132). Predict 301k;
  "moves toward 318,748" is unfalsifiable.
- Rev 1's P2 (Q9 → MATCH) was **over-confident**: on the reviewer's
  synthetic reproduction the estimate corrected exactly while **the plan
  shape did not change at all**. Correcting cardinality ≠ changing
  shape ≠ matching PG's shape.

## 7. Gates

1. Consumer enumeration (P2) done and written down **before** the edit.
2. Suites green; `go vet`.
3. Focused pins: a `NOT VALID` FK is consumed at the selectivity site;
   whatever decision is made at the proof site is pinned either way;
   `NotEnforced` still excludes.
4. P0 bit-identity on both corpora.
5. REPORT.md → agent review → `commit -n` + push.
