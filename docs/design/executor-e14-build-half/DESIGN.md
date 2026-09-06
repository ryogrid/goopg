# E-14 — EX1 build-half redesign: reader-set retention, no second truncation

Item: TODO_ALL `E-14 EX1 build-half redesign (no second truncation)`.
Predecessors: `docs/design/executor-ex1-04-owned/DESIGN.md` (the design the
review declined), the unblock review record (`134324df6` +
`analysis/planner-refactor-take3/ex104-cut0-20260904/README.md`),
`docs/design/planner-p4-01-target/DESIGN.md` (what landed instead),
`docs/design/executor-ex3-02-dense-build/DESIGN.md` (where the retained row
physically lives), `docs/design/executor-e09a-shared-spilling-build/DESIGN.md`
and its `DESIGN-E09b.md` (who else reads a retained row).
Blocks: E-12 (EX3-02 Cut 3 — arena sizing over the redesigned build rows),
D-05/D-06 geometry.

PG oracle for every rule below: `postgres/src/backend/executor/nodeHash.c`
(`ExecHashTableInsert` stores a *MinimalTuple* built from
`ExecFetchSlotMinimalTuple`, i.e. the inner **tlist**, never the scan tuple)
and `postgres/src/backend/executor/nodeHashjoin.c`
(`ExecHashJoinOuterGetTuple` / `ExecScanHashBucket`, where
`hjclauses` are re-checked and `ExecProject` at the join node decides what
survives). PG's structural advantage is that a hash join is a `ScanState`
with its own projection: `ps_ProjInfo` drops any inner column the parent
does not read, so PG never carries the inner key column past the join.
goopg's `Join` has no output projection — its output is a fixed
`Left ++ Right` concat — and that single missing capability is the whole
subject of this document.

---

## 1. What the review ruled out, and why it was right

EX1-04's original mechanism was: at each retention site, copy `[0, bound)`
instead of `len(schema)`, where `bound` is the leaf's threaded
`deformBound`. Schema and coordinates deliberately UNCHANGED.

The review (ledger `take3-EX1-04-blocked`) declined it. The failure is not
"the bound is computed wrong"; it is that a **row shorter than the schema
its consumers address is a different type of value**, and goopg has readers
that address by schema position without ever seeing the bound:

- `slotRow()` / `Row()` / `Materialize()` above the retainer flatten the
  composed row at `len(schema)`;
- null-fill width variance: `nullRow(o.lazyRW)` is built from the SCHEMA
  width and is bound into the same slot field as a retained row
  (`o.lazyBuildSlot.row`), so a truncated retained row and a full-width
  null pad alternate through one reader;
- batch geometry prices schema widths (`hashsize.EntryBytes = f(ncols)`),
  so the executor would hold rows the planner does not price;
- and the tail of a truncated row is not NULL, it is ABSENT — an
  out-of-range index, not a wrong value, which is a panic in some readers
  and a silent stale read in others.

The precedent this repository already carries says the same thing from the
other side: the previous attempt at a narrower Datum returned **0 rows on
seven TPC-H queries** from arena slot reuse after passing a five-query
gate. Width and lifetime changes on retained rows are a silent-corruption
class, not a performance class.

**The rule this design adopts: no retained row may ever be shorter than the
coordinate space its readers address. If the payload narrows, the reader
coordinates move with it, in the same commit, at exactly one seam.**

---

## 2. What P4-01 changed, and the residual it left

P4-01 slices 1–3 landed `narrowBuildInput` (`internal/optimizer/narrowoutput.go`):
a hash join's build input is wrapped in a `Project` whose `Targets` are the
derived keep-set, and **row and schema narrow together**. That is the
rev-7 proven-safe shape. It bought Q9 20.14→13.88 s and −10% allocation
with zero executor risk (Cut 0), and EX1-04 Cut 1 pinned the resulting
shape with five poison tests (`owned_build_poison_test.go`), implementing
no narrowing of its own.

So the build half is not "un-narrowed". The question is what is LEFT.

Measured on the in-package Q9 fixture (`obpQ9Catalog` / `obpQ9InnerSQL`,
the same fixture the Cut-1 poison tests plan through), listing every hash
join's build side after P4-01 narrowing, with the columns the join's own
key pairs consume:

| join | build side (post-Project) | w | key columns | read above the join |
|---|---|---|---|---|
| root | `Project[ps_partkey, ps_suppkey, ps_supplycost]` | 3 | ps_partkey, ps_suppkey | ps_supplycost |
| 1 | `Project[p_partkey]` | 1 | p_partkey | — (none) |
| 2 | `Project[l_orderkey, l_partkey, l_suppkey, l_quantity, l_extendedprice, l_discount, n_name]` | 7 | l_orderkey | the other 6 |
| 3 | `Project[s_suppkey, n_name]` | 2 | s_suppkey | n_name |
| 4 | `Project[n_nationkey, n_name]` | 2 | n_nationkey | n_name |

**6 of the 15 retained columns are this join's own key columns that nothing
above the join reads.** They are in the keep-set because the executor
evaluates the build key expression *against the build row*, so the planner
cannot drop them: they must exist at build time. They are dead the instant
`evalHashKeyDatumSlot` returns — the value is now the map key — unless the
`execResidual` re-check or something above reads them.

That is the whole residual, and it is not expressible as a `[0, bound)`
prefix: every one of those columns is at position 0 of its build side.
**A prefix truncation cannot even describe the remaining opportunity.**
This is the concrete reason the original mechanism "cannot simply be
resumed", independent of its safety.

Size of the residual, using D-05's measured entry width
(`analysis/minimize-datum/d05-entrywidth-20260906/README.md`): a 2-column
retained row is 120 B (2×48 + a 24 B slice header) and two batches need
≤111.8 B/row. Dropping one dead key column from a 2-column build gives
72 B/row — **below the two-batch threshold**, i.e. a predicted `nbatch`
4→2 on exactly the shape D-05 measured. That is why E-12 (arena sizing)
and D-05 (geometry) are sequenced behind this item.

---

## 3. The shape that replaces the second truncation

### 3.1 Reader-set retention, not bound truncation

Retention keeps the columns some reader of the composed output actually
addresses. The retained payload is a **gather** over an ascending keep-set,
not a prefix; and the join **moves the reader coordinates onto it at one
seam** — `ensureLazyVirtual`'s `[]virtualCol` map, which is already the
sole translation between "build-side storage position" and "output
position".

```
sources = [probe, build(narrow), nullPad]      // nullPad is a 1-wide NULL row
cols[i] = (probeSrc, i)            for probe positions
cols[i] = (buildSrc, remap[i])     for retained build positions
cols[i] = (nullPadSrc, 0)          for dropped build positions
```

Nothing else in the executor learns a new coordinate space. `Width()` of
the composed slot is unchanged (`len(cols)` = the schema width), so
`Materialize()`, `Row()`, `slotRow()` and every reader above the join see
exactly the shape they see today. **The row is never shorter than what its
readers address; only the storage behind unread positions is gone.** That
is the difference from the declined design, stated operationally:

| | declined EX1-04 | this design |
|---|---|---|
| retained payload | `row[0:bound]` (prefix) | `gather(row, keep)` (set) |
| composed width | shrinks | unchanged |
| reader coordinates | unchanged (hence the bug) | remapped at one seam |
| unread positions | absent | present, NULL |
| geometry | priced at schema width | priced at retained width |

### 3.2 Where the keep-set comes from — and the two cuts

The keep-set is `{ columns some reader addresses }`, and the readers of a
retained hash-build row are exactly three:

1. `o.execResidual`, re-evaluated per match through `lazyVirtualOut`;
2. the emitted composition — every build position that reaches the join's
   OUTPUT and is then read by anything above the join;
3. the RIGHT/FULL unmatched-build sweep, which emits through the same map
   (so it adds no columns beyond (2)).

Reader (1) is local to the join: `execResidual` is a field, its references
are in merged coordinates, and a fail-closed whitelist walk over it (the
`deformScanRefs` pattern from `scan_deform.go`) either returns the exact
set or declines.

Reader (2) is NOT local for INNER/LEFT/RIGHT/FULL: the join's output is
`Left ++ Right` and the executor has no idea what its parents read. That
is the missing projection PG has and goopg does not.

**Reader (2) IS local — structurally, not by analysis — for Semi and
Anti.** `optimizer.Join.Output()` returns `Left.Output()` for
`JoinTypeSemi`/`JoinTypeAnti` (`internal/optimizer/plan.go:1189-1196`), and
`buildLazyHashTable` forces `buildLeft = false` for those two types. The
build side is therefore the RIGHT side, and **no build column appears in
the join's output at all**. Emission goes through `lazyOuterOnlyOut`, an
identity map over the probe. So for Semi/Anti the entire keep-set is
`refs(execResidual) ∩ build side` — zero columns when the hash key folded
every conjunct in.

This splits the item cleanly:

- **Cut A (this commit).** Semi/Anti hash builds. Keep-set derived from
  `execResidual` alone, fail-closed. Proof of reader-completeness is
  structural (`Output() == Left.Output()`), not a walk that can miss a
  shape. Retained width drops to `|refs(execResidual) ∩ build|`, which is
  **0** for a pure equi Semi/Anti join.
- **Cut B (specified here, deferred).** INNER/LEFT/RIGHT/FULL. Needs
  reader (2): a Build-time top-down keep-SET walk (the generalisation of
  `scan_deform.go`'s prefix bound to a set) threaded through
  `executor.go`'s `buildNode` / `buildRec` in parallel with `deformBound`,
  and the same join-layout per-side mapping `deformJoinBounds` already
  performs. Deferred for two reasons stated in §8.

### 3.3 Why Cut A is worth its own commit

Semi/Anti hash joins are where the biggest builds are: TPC-H Q4, Q16, Q18,
Q20, Q21, Q22 all put an `EXISTS`/`NOT EXISTS`/`IN`/`NOT IN` build side
under a hash join, and Q21/Q22 build over `lineitem`/`orders` scale. A
pure equi Semi/Anti build goes from `24 + w*48 + payload` bytes per row to
`24` — the `[]Row` slice element and nothing else. The rows are pure
presence markers and always were; today goopg pays a full row copy,
a stratum-D cell extent and a stratum-B payload copy for each one.

---

## 4. Interaction with E-09a (shared spilling build) and E-09b (shared reload)

Both landed today, in the files this item edits, and neither is in the
design this item resumes from. Three interactions, each pinned:

**(a) The descriptor is immutable and shared; the retained width is part of
the build's identity.** `captureSharedBuild` publishes `leftWidth` /
`rightWidth` (the SCHEMA widths, from the child schemas) and
`applySharedBuild` restores them. The retained width is a THIRD width and
must not be conflated with either: `lazyLW`/`lazyRW` stay the schema widths
(they size the output composition and the key slots), while the retained
width sizes only the storage behind the build source. A participant that
adopts a published table does not run the build loop, so it must derive
the retain plan itself. It does — the derivation is a pure function of the
plan node (`Type`, `execResidual`, `lazyLW`), which is identical in every
participant — and `sharedHashBuild` carries `retainWidth` so the
participant ASSERTS agreement rather than assuming it. A mismatch is an
`XX000`, not a silently mis-addressed row.

**(b) Batch files are written by one participant and read by others.**
E-09a's rule is that the leader's prebuild writes every inner file and
freezes them (`innerShared`, `growEnabled=false`); participants only read.
The rows in those files are retained rows, so they are written at the
retained width and read back at the retained width. The reader learns the
width from the frame itself (`appendRowPayload` writes
`uvarint(len(row))`), so the round trip is self-describing and no width
travels out of band. What MUST agree is not the decode but the
INTERPRETATION: the reader's `virtualCol` map must be the one that matches
the writer's keep-set. (a)'s assertion is what makes that true.

**(c) E-09b's one-live-batch-table.** `sharedBatchLoad` has the first
participant to reach batch *k* load it and the rest adopt the same maps
behind a refcounted, cancellation-aware wait. Those maps hold retained
rows, so every adopter is reading rows narrowed by the loader's keep-set —
again the same pure function, again covered by (a)'s assertion. E-09b
changes the LIFETIME of a reloaded batch (shared instead of private), not
its shape, so no additional rule is needed; but it does mean a width bug
would now corrupt every participant instead of one, which is why (a) is an
assertion and not a comment.

Cut A does not touch `freezeForSharing`, `newParticipantBatchState`,
`sharedBatchLoad`, the exactly-once-open counters or the cancel path. Their
gates must still pass unchanged, and are re-run.

---

## 5. The spill round trip

`appendRowPayload`/`encodeDatum` and `ReadRowInto`/`decodeDatum` are the
sibling pair a codec change once broke in six regress tests at once. This
design **does not change the codec**: it changes only which Row is handed
to it.

- `WriteRowHashed(h, row)` writes `uvarint(len(row))` then one encoded
  Datum per column. A narrowed row is a shorter, entirely well-formed
  frame. A zero-width row is `uvarint(0)` and no datums — legal by the
  format, and exercised by an explicit round-trip test rather than
  assumed.
- The decoder allocates by the count it reads, so it reconstructs the
  retained width without being told. **The round trip is the test**, in
  both directions and at width 0.
- `increaseNumBatches` evicts in-memory rows to files and
  `loadInnerBatch` reads them back; both move retained rows, so both stay
  consistent by construction.
- `estimatedRowBytes(row)` is called on the retained row. It becomes
  HONEST (it measures what is actually held), which is the intent — the
  in-memory budget stops charging for storage that no longer exists.
  `TestEstimatedRowBytesAgreesWithEntryBytes` pins the FUNCTION against
  `hashsize.EntryBytes` for a given Row; passing a narrower Row does not
  move that agreement and the test stands unmodified.

### 5.1 The model/reality gap this opens — reported, not fixed

The planner's `hashsize.EntryBytes` prices `ncols` from the build node's
SCHEMA. After narrowing, the executor holds fewer bytes than the planner
prices, so the planner **over-estimates** and may choose more batches than
the executor needs. That direction is conservative (never an under-sized
build, never an unplanned OOM), so it is safe to land, but it forfeits
part of the geometry win this item exists to unlock.

`internal/executor/hashsize/` is owned by another agent for this session.
**Required follow-up, to be taken with D-05:** `EntryBytes` needs to price
the RETAINED width, i.e. the planner needs the same Semi/Anti rule
(`retained = |refs(residual) ∩ build|`). Until then the flag's geometry
effect is bounded by whatever the planner already chose.

---

## 6. Mechanism (Cut A, as built)

New file `internal/executor/join_retain_narrow.go`:

- `joinRetainNarrowOn` — process flag, **opt-in** (`GOOPG_JOIN_RETAIN_NARROW=on`),
  plus a test-only setter. Default OFF makes this commit behaviour-neutral
  at HEAD by construction; the flip is its own measured commit (§7).
- `buildRetainPlan{keep []int32, width int}` and
  `(*joinOp).planBuildRetain()`, the pure derivation. Declines (returns
  nil ⇒ exact pre-change path) on: flag off, nil plan, non-hash algo,
  `Lateral`, join type not Semi/Anti, `BuildLeft` (a defensive
  contradiction of the Semi/Anti contract), `preserveBuildSide` (the
  FOR UPDATE ctid build does not retain through this path at all), a
  residual reference the whitelist does not positively understand, a
  reference out of range, and the no-op case where the keep-set is the
  whole width.
- `retainNarrowRow` — gather into a per-join scratch buffer, then hand to
  the existing `retainBuildRow` so EX3-02's strata, the F1 Buf rule and
  the M0097-0058 detach contract are all untouched. **The detach property
  is preserved because the gather copies Datum structs into a scratch and
  `retainBuildRow` then performs the same arena/heap materialisation it
  performs today** — narrowing changes the count of Datums it materialises,
  never how one is materialised.

`operators_join_agg.go`:
- the retain plan is derived in `buildLazyHashTable` AFTER the CTID
  decision and cleared on every path that does not use it;
- the four `o.retainBuildRow(...)` call sites in the build loops route
  through `o.retainForBuild(...)`;
- `ensureLazyVirtual` gains the nullPad source and the remap;
- the build-side NULL pad is sized at the retained width (it is bound into
  the same slot field as a retained row).

`parallel_hash_build.go`: `sharedHashBuild.retainWidth` + the agreement
assertion in `applySharedBuild`.

---

## 7. Acceptance

Cut A lands OFF, so its acceptance at HEAD is "behaviour-neutral":

- `go build ./...`, `go vet ./internal/executor/`, full
  `go test ./internal/executor/` green (no `-count=1`).
- `go test -race` on the parallel hash tests; the only tolerated failure is
  the pre-existing `TestSubquerySemanticsMatrix/M20` `instrumentScope`
  race (ledger `take3-instrumentscope-datarace`).
- E-09a/E-09b gates unchanged and green: forced-shape values per join type
  at batching `work_mem`, poison-writer, exactly-once-open, cancel-mid-batch.
- Flag-ON unit gate: keep-set derivation table (including every decline),
  forced Semi/Anti shapes with values compared ON vs OFF at a batching
  `work_mem` so the spill round trip is exercised, a zero-width spill
  round-trip pin, a poison pin on the Project shape extended to narrowed
  retention (dropped positions must read NULL through the composed slot
  and must never alias the producer), and a retained-width/alloc pin.

The flip to default ON is a SEPARATE commit and needs the bench arm the
TPC-H cluster is currently holding (§9): Q21/Q22 alloc arm
(`inuse_space`, allocation COUNT first), values 24/24, plan pin 22/22,
TPC-DS SF0.5 `CKMISMATCH=0`, and per-query timing for everything whose
plan or batch count moves — a row-count gate cannot catch a plan-shape
regression (21/21 byte-identical while Q2 went 43× slower).

---

## 8. Cut B — specified, deferred, with the resume point

Cut B is INNER/LEFT/RIGHT/FULL, where the residual dead weight measured in
§2 lives (6 of 15 columns on Q9). It needs reader (2): "which of my build
side's columns does anything ABOVE this join read".

The mechanism is a keep-SET version of the walk `scan_deform.go` already
performs for a prefix bound: `deformBoundBelow` threads top-down through
`buildNode`, and `deformJoinBounds` already maps above-refs through the
join layout per side (`Index < leftWidth` → left, else right at
`Index-leftWidth`), declining both sides to full on anything it does not
positively understand. Generalising `int` to `[]int32` there is mechanical;
what is not mechanical is that it must be threaded through **every**
`buildNode`/`buildRec` arm in `executor.go` in parallel with the existing
bound, and both build paths (the tree and the slab twin) must agree — the
sibling-paths-must-agree class.

Deferred because:

1. It edits `executor.go` broadly while two other agents hold adjacent
   executor/optimizer files this session, and
2. its value is a batch-geometry change, which is exactly what CANNOT be
   validated without the TPC-H cluster — and a geometry change validated
   only by row counts is the 43× class.

Resume point: this section plus §3.1's table; the walk to generalise is
`scan_deform.go:deformJoinBounds`; the seam is unchanged from Cut A.

---

## 9. Rejected alternatives

- **Null out the dead cells in place (keep the width).** Perfectly safe —
  no coordinate move at all — and nearly worthless: it saves the stratum-B
  payload bytes but not the 48 B Datum cell, and every dead column on the
  Q9 witness is an integer key with no payload. Recorded because it is the
  obvious cheap move and it does not pay.
- **Reconstruct the dropped key column from the hash-table key at emit
  time.** The int64 lane's key IS the canonicalised value and the string
  lane's key is a canonical encoding, so the column looks recoverable. It
  is not, safely: the canonical form is lossy about Kind (int4 vs int8 vs
  numeric) and about the original datum for date/numeric, and rebuilding a
  Datum from a lossy canonical form to feed the client is a wrong-values
  class, not a performance class.
- **Keep the prefix bound and reorder the Project's targets so dead
  columns fall at the tail.** Moves the problem into the planner's
  coordinate space (`buildKeepSet`'s ascending/unique/in-range guard exists
  precisely to prevent permutations) and re-opens the P4-01b
  wrong-narrowing class for a strictly weaker result than §3.1.
