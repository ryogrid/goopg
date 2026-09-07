# D-06 / MD-05 — packing the sort's retained rows

*TODO_ALL row: `docs/design/not_ralph/minimize_datum/TODO_ALL.md` D-06.
Bundle design: `04-target-design.md` §4.1 (Tier A, the `sort rows []Row`
line) and §9.12 (R-11). Gate: `06-verification.md` §3 MD-05.*

Status: design + two adversarial reviews (2026-09-07, re-verified at
`fc76b20fd` — the tip's two merge commits touch only docs, `initdb`,
`flaglabels`/`joinsearchseam`/`onerelsearch` and `planner-flags.env`, so
every executor citation below holds within ~10 lines).

---

## 0. What this row is, and the stopping rule it inherits

MD-05 converts the **one** Tier-A sort retention site —
`sortOp.rows []Row` (`internal/executor/operators.go:803`) — from `[]Row`
(a `[]Datum` per row, 48 B per cell) to `[]PackedTuple` (PG MinimalTuple
layout, `packedtuple.go`), consumed through the lazy-deform `*PackedSlot`
that D-03 landed with no producer.

It inherits a **stopping rule that has already fired once**. D-04
(MD-03.5, the throwaway prototype on the hash build) measured, on this
tree and with the encoder this row would use:

| number | D-04 result |
|---|---|
| batches | 4 → 4 **unchanged** |
| retained bytes | −14.2 % (accounting) / −24.4 % (`inuse_space`) |
| wall time | **+6.8 %**, n=7 per arm |
| allocations | **+39 %** (`EncodeRowPGCtx` ≈ 6 allocs/row against ≈ 1 for the legacy retain) |

and 05 §6's rule in its own words: *"batches unchanged → the model in D-3
is wrong. Fix the model before touching another site."* The model has NOT
been fixed — D-05, the row that would have fixed it, went out of scope on
2026-09-07.

**This design therefore does not assume MD-05 is a win.** It is written so
the conversion is measured against its own arm at its own commit and
**reverted as a unit** if the same numbers come back the same way. Every
structural change sits behind one switch (§7) whose OFF path is
byte-for-byte the code at `c67051743`.

What is *different* about sort — and why the D-04 answer cannot simply be
transferred — is §2 and §3.

---

## 1. Scope, and what B-01c did and did not give this row

B-01c's applying slices (b) and (c) landed the sort-side projection D-06
was blocked on. The boundary is exact, and this design must be correct on
both sides of it:

* **Narrowed**: a sort whose ancestor chain reaches a `*Project` or
  `*Aggregate` through only `*Filter` / `*Limit` / `*Sort`.
* **Declines, keeps today's full width** (the majority path): a chain
  meeting `*Distinct` / `*DistinctOn` / `*Gather` / `*GatherMerge` / a
  join / `*Result` / `*CTEScan` / `*Memoize` / `*WindowAgg`; a Sort at the
  plan root; a Sort with two parent edges; a keep that is the identity.

The conversion in this doc is **width-agnostic**: it changes the
representation of whatever row the sort is handed, not which columns it is
handed. It is therefore correct on both sides of the B-01c boundary by
construction, and no part of it inspects a narrowing decision.

The one place the boundary *matters* is the prize (§3): narrowing already
removed the columns whose packing would have paid most, exactly as D-04
found for the hash build (*"EX1 narrowing already landed on this build
half: 120 B/row, 2 columns, so the bundle's ~5× width premise is 1.9×
post-EX1"*). §3 re-derives that for sort rather than borrowing it.

---

## 2. R-11 is discharged at HEAD, and the register entry is stale

04 §9.12 (R-11) reads:

> A comparator needs both operands' keys; a `PackedSlot` has **one**
> scratch `Row`. PostgreSQL solves this with `SortTuple.datum1` — §2.4
> *names* `datum1` and then does not propose it. Without an equivalent,
> every comparison in an O(n log n) sort deforms two tuples from scratch.
> **MD-05 is therefore not "mechanical"** … It needs a hoisted-key design
> (PG's, or precomputed `sortKeyVals` retained alongside the packed tuple
> — which `sortOp` already computes). Re-price before starting.

**The parenthesised mitigation is not a proposal; it is the code.**
M0134-0191 landed `sortOp.keyvals [][]Datum` (`operators.go:818`) and made
it the *only* comparator input:

* `lessKeyVals(a, b []Datum)` (`:1044`) takes key values, never rows.
* `sortChunk` (`:1078`) sorts a **permutation** whose `Less` closes over
  `o.keyvals`, never over `o.rows`.
* `sortTailWithCTIDs` (`:1125`) does the same.
* the merge heap (`sortHeap.Less`, `:1532`) compares `sources[i].curKeys`,
  filled by `sortSource.loadKeys` (`:1504`) — again never a Row.

goopg stores **all k** keys where PG stores only `datum1`, and the reason
is recorded in `docs/design/not_ralph/parallel-sort/DESIGN.md` §4.1: an
interpreted `evalExpr` dispatch per key is far more expensive than
`heap_getattr`, so goopg's complexity tradeoff lands on the other side of
PG's. **goopg is already strictly past `SortTuple.datum1`.**

So *no comparison in the sort path deforms anything*, before or after this
change, and the O(n log n) deform cost R-11 prices does not exist.

### 2.1 What R-11 should have said — three real two-row sites

Re-pricing R-11 does not make MD-05 mechanical. It relocates the hazard
from the comparator, where it is absent, to three places R-11 never
mentions. All three are **liveness**, not comparison.

**(a) `popMerge` invalidates the row it is about to return.**
`operators.go:1433`:

```go
s := heap.Pop(o.heap).(*sortSource)
row := s.cur
if err := s.advance(); err != nil { ... }   // ← would overwrite the scratch
...
return row
```

Today `s.cur` is an owned `Row`, so this is safe. If the in-memory tail
source deforms into a slot's scratch `values`, `advance()` rewrites the
very row `popMerge` returns. That is a **silent wrong answer**: the caller
gets the *next* row's contents at the *popped* row's ordering position. It
is precisely one row of "two deformed at once", and it is the one that
bites.

*Decision:* the packed in-memory source deforms into **two alternating
scratch slots** (`cur` taking the one the previous row did not). At most
one row per source is outstanding at any instant — `popMerge` returns
before the next `Pop` — so a double buffer is exactly sufficient and
allocates nothing in steady state. A third buffer would be dead.

**(b) The `lessRows` defensive fallback needs two Rows.**
`sortChunk` (`:1079-1083`) and `sortTailWithCTIDs` (`:1125-1140`) fall back
to `sort.SliceStable(rows, … o.lessRows(rows[i], rows[j]))` when
`len(o.keyvals) != len(rows)`. `lessRows` (`:1270`) re-evaluates **both**
operands' keys from the Rows, so under packing it is the genuine "two
deformed rows at once" call — inside the comparator, O(n log n) times.

*Decision:* under the packed arm this fallback is **removed, not ported**.
The condition it guards is an internal invariant violation, and the packed
arm makes it structurally impossible: `packed` and `keyvals` are appended
in the same statement, truncated in the same statement, and permuted by
the same `applySortPerm`. Under the switch, a mismatch raises a latched
error rather than silently sorting by a *second* comparator — which is the
very wrong-answer class the file's own `sortKeyVals` comment
(`:1010-1015`) warns about and which 06 §3 makes MD-05's named gate.
Degrading to a different comparator is not a safe fallback for a sort; it
is the bug.

**(c) `flushChunk` and the spill format.**
`flushChunk` (`:1315`) calls `w.WriteRow(r)` per row and `spill.go`'s codec
takes a `Row`. The on-disk payload is **MD-last / 03 TD-5** and explicitly
outside this row, so the packed arm deforms each row back to a `Row` at
the flush boundary and writes today's format unchanged. One deform per
spilled row, once, on a path already doing file I/O; the read-back side
(`sortSource` with a `reader`) is untouched and still yields owned Rows.

**Conclusion for the register.** R-11's *verdict* ("not mechanical,
re-price") stands; its *mechanism* is wrong at HEAD. The correct entry is
(a) + (b) + (c), and the double buffer in (a) is the piece 05 §4's LOC
price does not contain.

---

## 3. The prize, modelled before it is measured

Per retained row, at HEAD, for a sort of width `w` over `k` keys:

| component | bytes |
|---|---|
| `[]Row` backing (slice header per element) | 24 |
| `Datum` cells | 48·`w` |
| varlena payloads | `V` |
| `keyvals` `[][]Datum` backing | 24 |
| `keyvals` `Datum` cells | 48·`k` |

After packing, the first three become one `PackedTuple` (`buf []byte` +
`extra uint8`, 32 B per slice element) plus one allocation of
`23 + bitmap + Σ attlen` — where `Σ attlen` is 8 B for an
int8/float8/date column against 48 B for its Datum cell.
**`keyvals` is unchanged and unpacked.**

Two consequences a reader of 04 §4.1 would not predict:

1. **`keyvals` is a floor this row cannot cross.** The key columns are
   retained **twice** after the conversion — once packed inside the tuple,
   once as Datums in `keyvals` — and the Datum copy is the expensive one.
   For the common shape `k` ≈ `w` (a narrowed `ORDER BY` over its own
   output, which is exactly what B-01c slice (c) produces), packing removes
   at most `48w + 24 − (32 + 24 + Σattlen)` of `48w + 48k + 48`, i.e.
   **under half** — and it approaches **zero as B-01c narrows harder**.
   The better the narrowing, the smaller this row's prize. That is the
   same premise-collapse D-04 measured, and here it is structural rather
   than corpus-dependent.
2. **Sort has no bucket table.** D-04's second reason the model was wrong
   ("peak live heap is 506 MB of hash-map buckets against 296 MB of rows,
   so the largest consumer in this join is not the retention format") does
   **not** apply here: `sortOp` retains a flat slice and nothing else, so
   100 % of its footprint is the format this row changes, minus the
   `keyvals` floor above. This is the one respect in which sort is a
   better candidate than the hash build.

Against that, the costs are D-04's, unchanged in kind: `formPackedTuple` →
`encodeValuePGCtx` per column per row on the way in, and a deform on the
way out (once per row; sort emits every retained row exactly once, so
unlike a hash build there is no probe multiplier in either direction).

**Predicted sign, stated before measuring:** bytes down by less than
D-04's −24 % (the `keyvals` floor), allocations up (same encoder, same
mechanism), wall time up. §8's measurement exists to falsify that
prediction, and the arm is built so a confirmation is a one-commit revert.

---

## 4. The conversion, site by site

`o.rows []Row` → `o.packed []PackedTuple` + `o.desc *TupleDesc`, under the
switch. `keyvals`, `ctids`, `wantCTIDs`, `spillFiles`, `heap`, `peakBytes`
and every EXPLAIN path are untouched.

| site | file:line | change |
|---|---|---|
| `Open` retain | `:944` | `FormPackedTuple(o.desc, row, ctx)` appended to `o.packed`; `keyvals` computed from `row` **before** packing, exactly as today, so key evaluation is bit-identical and the comparator cannot drift |
| `chunkBytes` | `:952` | still `estimatedRowBytes(row)` on the pre-pack `Row` — the accumulator feeds `peakBytes`, which EXPLAIN reports, and changing what it measures inside this commit would confound the arm. §8 reports packed bytes separately |
| `sortChunk` | `:1078` | permutation `Less` unchanged (`keyvals`); the permutation is applied to `packed` |
| `applySortPerm` | `:1098` | gains a `[]PackedTuple` arm; all slices still move under ONE permutation |
| `sortTailWithCTIDs` | `:1125` | unchanged except the slice it permutes |
| the `lessRows` fallbacks | `:1079`, `:1131` | latched error under the switch (§2.1(b)) |
| `flushChunk` | `:1315` | deform → `WriteRow`, on-disk format unchanged (§2.1(c)) |
| `Next`, in-memory | `:1367` | `o.outSlot.Load(o.packed[o.idx])`; return the `*PackedSlot` — the lazy deform is the point: a consumer reading 2 of 12 columns deforms a 3-column prefix. The ctid re-attach writes the `PackedSlot`'s own ctid triple (R-0 sites 4/5 already carry arms) |
| `initMerge` tail source | `:1400` | tail source carries `packed` plus the double buffer (§2.1(a)) |
| `Close` | `:1343` | `o.packed = nil`, slots released |

`o.desc` is `NewTupleDesc(o.Schema())`, built once in `Open`. `o.outSlot`
is `NewPackedSlotForSchema(o.Schema(), o.desc, …)` — the *ForSchema* form,
because the descriptor does not carry `SourceTableIdx` and
`findColumnIndexByNameAndSource` needs it to disambiguate a self-join
above the sort.

**Fail-closed on an unpackable row.** `FormPackedTuple` errors for a type
the encoder cannot represent (D-02 ledgered `take3-D-02-enum-encode` as a
live example). Under the switch that error **fails the query** rather than
falling back per row: a per-row fallback would put two formats in one
sort and force every downstream site to handle both, which is how a
sibling-path divergence starts (`pattern_sibling_paths_must_agree`).
D-02's census reports 0 declining columns of 160,302, so the loud path
should never be taken; if it is, that is a finding, not a degradation.

---

## 5. The ordering gate (06 §3 MD-05)

06 §3 names one thing for MD-05: *the comparator warning* (at HEAD,
`operators.go:1010-1015`) — a chunk sorted by one comparator and merged by
another emits out-of-order rows **with no error**, so sort's gate checks
ordering explicitly, not just membership.

A row-set assertion passes on wrong output. Every test in this slice
therefore asserts the **sequence**:

1. `ASC NULLS LAST` (the ASC default), `ASC NULLS FIRST`,
   `DESC NULLS FIRST` (the DESC default), `DESC NULLS LAST` — four
   single-key cases, each with NULLs present **and** duplicate keys
   present, so stability is observable too.
2. A **mixed** multi-key case (`a DESC NULLS LAST, b ASC NULLS FIRST`) —
   the case a comparator that dropped a per-key flag still passes every
   single-key test on.
3. Each of the above **twice**: fully in-memory, and with
   `chunkLimitBytes` forced small enough to spill ≥ 2 chunks, so
   tail-vs-merge comparator agreement is exercised.
4. **Arm equivalence**: the same input through the switch OFF and ON must
   produce the identical *sequence*, not the same set — this is what
   catches a permutation applied to `packed` but not to `keyvals`.
5. The `ctid` side-channel: `ORDER BY … FOR UPDATE` in-memory, each
   emitted row's TID matching its own row after the sort (the
   `applySortPerm` lockstep, now across three slices).
6. **Liveness**, the §2.1(a) hazard, as a test rather than a comment: a
   spilled sort whose merge draws alternately from the packed tail and a
   spill file, with every emitted row's *contents* asserted — a
   single-buffer implementation returns the wrong row's data here and
   passes every ordering assertion in 1–3.

---

## 6. What is deliberately NOT in this row

* the on-disk spill payload (MD-last / 03 TD-5) — §2.1(c);
* `keyvals` itself (removing the double retention needs PG's `datum1` plus
  an abbreviated-key design, which 04 §2.4 does not propose) — §9;
* `hashsize` / planner geometry — sort's budget is a fixed 256 MiB
  constant (`sortChunkBytes`, `:898`), not a planner-modelled one, so
  there is no `nbatch` analogue to move and no geometry half to forfeit;
* the `*WindowAgg` narrowing SITE (ledger `take3-B-01c-applying-blocked`),
  which is B-01c's row, not this one.

---

## 7. The switch, and why the A/B is same-binary

`plan_snapshots/c20a-c06s-plancost-rows-20260907.txt` has drifted from HEAD
(C-19's base-rel scan repricing landed after the pin was cut), so a pin
diff cannot be the instrument. The arm is **same-binary, same-commit**: one
process-level switch read once per `sortOp.Open`, default OFF at the
measurement commit, so both arms are the same bytes and the only
difference is the branch.

This also discharges the revert story the D-04 stopping rule requires: if
§8 reproduces D-04's numbers, the row lands as a **measured negative**, the
switch stays OFF, and no other site has been touched.

---

## 8. Acceptance

Floor (06 §3): TPC-H values 24/24, TPC-DS SF0.5 PASS=95 all-zero,
`scripts/tpch-spotcheck.sh` Q12=2 / Q13=35, `-race` green, plan check.

Per 06 §3's per-slice requirement:

* **alloc arm** — allocation *count* and `inuse_space` (never
  `alloc_space` for a retained-heap comparison), OFF vs ON, on a
  sort-heavy shape at a held server age;
* **ordering arm** — §5, which can fail the slice on its own;
* **the three D-04 numbers** — retained bytes, wall, allocs — reported
  against §3's prediction whichever way they come out.

---

## 9. Successor filed by this design

**`keyvals` is the sort's larger retention after this row.** For a
narrowed `ORDER BY` over its own output the `[][]Datum` key table is a
comparable or larger footprint than the packed tuples themselves (§3), and
nothing in the bundle addresses it: 04 §2.4 names PG's `SortTuple.datum1`
only to explain why goopg diverged from it. The real successor is an
**abbreviated key** (PG's `SortSupport` `abbrev_converter`) — a single
`int64` per row per leading key with a full comparison only on tie — which
would take the key table from `48k + 24` B/row to 8 B/row *and* make
comparisons cheaper. That is a larger, self-contained item and is not
attempted here.

---

## 10. Adversarial review (2026-09-07)

Two reviews were run against §§1–9 above: a source-falsification pass over
`internal/`, and a PG 18.3 oracle pass over `postgres/` (read-only).
Their corrections are recorded here, inline, in the words of what changed.

### 10.1 Source-falsification pass (`internal/`, at `fc76b20fd`)

All load-bearing citations hold:

- **R-11 discharge (§2): CONFIRMED.** `sortChunk` (`operators.go:1078`)
  sorts a permutation whose `Less` closes over `o.keyvals` (`:1090`);
  `sortTailWithCTIDs` (`:1125`) does the same (`:1143`); the merge heap is
  built with `less: o.lessKeyVals, keysOf: o.sortKeyVals` (`:1403`) and
  orders `curKeys` (`sortSource.loadKeys`, `:1504` region). No comparator
  touches a `Row`. goopg retains all `k` keys where PG keeps `datum1` —
  strictly past it, as §2 claims.
- **§2.1(a) hazard: CONFIRMED VERBATIM.** `popMerge`
  (`operators.go:1432-1448`): `row := s.cur` followed by `s.advance()`
  before return. A scratch-deforming in-memory source would hand back the
  next row's contents at the popped position — silent wrong answers. The
  double-buffer decision stands; §5 test 6 is the load-bearing test.
- **§2.1(b): CONFIRMED.** `sortChunk` (`:1079-1083`) and
  `sortTailWithCTIDs` (`:1125-1140`) both fall back to `lessRows` on a
  `len(keyvals) != len(rows)` mismatch, and `lessRows` (`:1270` region)
  re-evaluates both operands' keys. Removing — not porting — the fallback
  under the switch is correct: a mismatch there is an internal invariant
  violation, and a second comparator is the exact wrong-answer class
  `operators.go:1010-1015` warns about.
- **§2.1(c): CONFIRMED.** `flushChunk` (`:1315`) calls `w.WriteRow(r)` per
  row; the spill codec takes a `Row`. Deform-at-the-flush-boundary keeps
  the on-disk format untouched (MD-last stays out of this row).
- **§4 table: CONFIRMED.** `Open` retain (`:944` region),
  `chunkBytes`/`peakBytes` accumulation (`:952` region), `applySortPerm`
  (`:1098`, all slices under one permutation), `Close` (`:1343`,
  `o.packed = nil` to be added), in-memory `Next` (`:1367`), tail source
  (`:1400` region). `sortChunkBytes` is still the hard-coded 256 MiB
  constant (`:898`) — no planner geometry to move, as §6 claims.
- **One correction, line-level:** `keyvals` lives at `:818` (struct) with
  the merge-heap copy at `:1469` (`sortSource`), not at a single site —
  §3's "retained twice" accounting already treats them as one floor, so no
  number changes.

No correction to the verdict: the prize prediction (§3 — bytes down by
less than D-04's −24 %, allocations and wall time up) is untouched by
review. §8's measurement is what can falsify it.

### 10.2 PG 18.3 oracle pass (`postgres/`, read-only)

- **`SortTuple.datum1` (§2): REAL** (`tuplesort.h:133-141`) — PG keeps the
  first key (or its abbrev) in the tuple header. goopg's all-`k` `keyvals`
  is the documented divergence (parallel-sort DESIGN §4.1), correctly
  characterised, not a gap this row must close.
- **Abbreviated keys (§9 successor): REAL** (`sortsupport.h:114-164`,
  `abbrev_converter`). The successor is a genuine PG mechanism, correctly
  scoped out of this row.
- **Fail-closed on unpackable rows (§4): consistent with the oracle.**
  PG's `form_minimal_tuple` has no per-row fallback either — a
  non-flattenable value is a caller-side error, not a second format.
- **No oracle objection to the double buffer (§2.1(a)).** PG's merge reads
  from per-tape `readBuffer`s that persist across advances; the two
  alternating scratch slots are the same guarantee in goopg's shape.
