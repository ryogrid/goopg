# EX1-04 Design — Owned-row narrowing on single-retainer chains

Item: `TODO_EXECUTOR.md` EX1-04 (gate: Q9 narrowed width recorded;
values + pin + alloc arm). Status: design for review.

## 1. What narrows (and what does not)

Width today is schema-concat, never deform-bound: every retainer
copies `len(schema)`. This slice shortens owned payloads to
`[0, bound)` — schema and coordinates UNCHANGED (P4-01b lesson) — on
TWO single-retainer chains only:

- sort input (`sortOp` rows/keyvals append): readers = sort keys
  (folded by the walk's Sort arm) + everything above the sort.
- hash build side (`ownedBuildRow` into `lazyHash`): readers = keys
  (positional pairs) + residual (merged split) + everything above.

Both reader sets are ⊆ the leaf's consumer set (leaf additionally
sees its local filter — a SUPERSET direction: the leaf bound is safe
for the retainer, possibly not tightest). So both retainers reuse the
leaf's effective bound, threaded at Build like `seqScanOp.deformBound`
— no second walk, no new coordinate space.

Everything else stays full-width: merged/NL outputs (`concatRows`),
agg states, gather transfer, materialize cache replay, DML/EPQ/lock
paths, spill codec. A second slice (if this one lands clean) extends
to merged outputs with per-segment bounds — explicitly not this
commit (merged-space readers are the long tail: `mergeResidualMatch`,
`join_merge_key`, NLI `BindOuter`, RETURNING collectors, CTID/resjunk
past the cut).

## 2. Mechanism

- `sortOp`/`joinOp` gain the bound at Build (same threading as scans;
  Gather closure capture applies to the whole subtree, retainers
  included). Retain sites (`sortOp` append, `ownedBuildRow`,
  `drainRowsCtx`) copy `[0, bound)` instead of `len(schema)` when
  bound < width; bound == width (or unset) takes the exact old path.
- `keyvals` lockstep: keys ⊆ bound by construction (walk folds them);
  poison asserts it (below).
- Batch geometry (`hashsize EntryBytes = 48·ncols…`) prices the
  narrowed width automatically (EX-P7 ordering) — `Batches:` may move
  on Q9; the gate records, not forbids (E2 roll-up if a plan moves…
  geometry does not move plans, only batch counts — state the
  expectation: same plans, fewer batches or equal).
- Poison tripwire: `poisonDeformTail` on retained rows under the flag;
  `checkDeformPoison` at the enumerated readers. A walk miss at
  retention scale panics in tests instead of corrupting a hash table.

## 3. Verification (gate)

- Unit: bound threading into sort/join ops, retain-width edges
  (bound==width passthrough, unset passthrough), poison runs over
  sort+build retain paths, keyvals-beyond-bound panics.
- Values: tpch-runner -diff 24/24 + TPC-DS PASS=95 + plan-gate 22/22.
- Q9 narrowed width re-recorded beside 1096/896/896/710/720
  (expect the BUILD-side widths to drop; merged outputs unchanged —
  state per-node expectations before running).
- Alloc arm MUST improve (this item's raison d'être: fewer/shorter
  owned rows) with timing neutral-or-better; else the item fails
  even with green values (ground rule 2).
