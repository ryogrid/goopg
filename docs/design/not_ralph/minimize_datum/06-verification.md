# 06 — Verification and acceptance

Gates for the work in 04, sized by 05. Nothing here is new tooling for its own
sake: where an instrument exists it is named and reused; where one does not, the
gap is stated.

---

## 1. The failure mode this bundle is most exposed to

A representation change that produces **correct row counts and wrong values**.

This is not hypothetical in this repository. Three of the bugs cited across 02
and 04 had exactly that shape:

- the `TimeSubtype` discriminator dropped from the spill codec — every spilled
  DATE became a bare timestamp; TPC-DS Q72 failed at small `work_mem` and passed
  at 2 GB (02 §1);
- `datumKey` reading `d.Int` on the big-numeric lane, so two equal numerics at
  different arena offsets hashed differently and a hash join **silently dropped
  pairs** (02 §8.3);
- the `*Slot` arm missing from `slotToRow`, so `CaseExpr`/`InExpr`/`FuncCall`
  fell to a `default: return nil` (02 §8.1).

**Consequence for every gate below: row counts are necessary and never
sufficient.** The primary gate is a value diff.

---

## 2. Per-commit floor

Every commit in the TODO, without exception:

| gate | command | pass |
|---|---|---|
| unit/component | `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | green |
| TPC-H spot-check | `scripts/tpch-spotcheck.sh` | canonical `Q12=2 / Q13=35` (`Q12=0 / Q13=2` is the known silent-regression signature) |
| **TPC-H values** | `cmd/tpch-runner -digest` / `-diff` against the pinned baseline | ordered **and** unordered digests plus column signature identical, all 22 |
| **TPC-DS values** | `scripts/tpcds-sf05-regression.sh sweep` against the git-tracked PG oracle | PASS=95, MISMATCH=0 **and `CKMISMATCH=0`** |
| **plan-shape pin** | `cmd/plan-snapshot` before/after | no plan moves, or the move is reported with its cost roll-up |

**`CKMISMATCH` is the row that matters and an earlier draft omitted it.**
The script reports value mismatches under a *separate* status from row-count
mismatches — `CKMISMATCH` is literally "the right number of wrong rows", which is
§1's failure mode by name. A pass condition of "PASS=95 MISMATCH=0" excludes the
one field that catches this bundle's primary exposure. Note also that a set of
queries report `ck=n/a` (saturated `LIMIT` windows) and are row-count-only; they
are not evidence for a values claim.

**The plan pin is skippable today and must be made non-skippable.**
`make plan-gate` exits 0 on SKIP when there is no baseline or `pg_isready` fails
on 65433 — a mandatory pin that silently passes is not a pin. Worse, its default
`structural` mode **strips `(cost=… rows=… width=…)`**, so a `hashsize` change
that moves costs without moving shapes is invisible, while R-6 asks for "the move
reported with its cost roll-up" — which structural mode cannot produce and
`semantic-cost` mode tolerates to ±10%. `plan-snapshot` is also TPC-H-only; there
is **no TPC-DS plan pin**. MD-01 (or an EX0 item) must close these before MD-04.

Never `-count=1` in a gate run. Never `git commit --no-verify` for code (the
`-n` authorised for this work is for **documentation** commits only).

The plan-shape pin is not optional here even though this is executor work: 04 §5
changes `hashsize`, which feeds the planner, so this bundle can move plans by
construction (R-6).

---

## 3. Per-slice gates

### MD-01 — `TupleDesc`

No behaviour change; the floor plus:

- **One transcription.** A test asserts that the `colTypeInfo` descriptor and
  `userTypeAttrsForOID` agree on `typlen`/`typbyval`/`typalign`/`typstorage` for
  every type both can name. 03 §5 says drift here is
  `pgPhysicalTypeIsVarlena`'s documented failure mode (PostgreSQL's
  `nocachegetattr` `Assert(j > attnum)`).
- **Against the oracle.** For the OIDs covered, the values match
  `postgres/src/include/catalog/pg_type.dat`. Cite the file, do not re-derive.

### MD-02 — the R-1 audit

Document only. It must report, over both suites:

- the count of plan nodes whose output schema contains a column
  `NewTupleDesc` **declines** (04 D-2), by decline reason. Note the decline is
  an allow-list check, **not** an encode error: the codec's outer default packs
  an unknown type as text and decodes it as `KindString` without complaint
  (04 §3.1), so an audit that measured encode failures would measure zero;
- the same count weighted by that node's estimated retained rows — a decline on a
  10-row node is free, a decline on the Q9 build side is the whole design;
- **every declining type name**, listed. Each one is also a latent on-disk
  retyping bug on a path that already ships, and belongs in the ledger whatever
  this bundle decides;
- an explicit verdict: **proceed / re-scope / stop**, in those words.

### MD-03 — `PackedSlot` with no producer

- **A test per type switch.** Six switches (04 §9.1), five of which fail
  silently. Each gets a test that constructs a `*PackedSlot` and drives that
  switch. `slotToRow`'s is the one with a committed precedent
  (`slot.go:247-252`).
- **Exhaustiveness, moved not copied** (03 TD-2). `TestSpillDatumRoundTripCovers
  EveryKind` and `…EveryTimeSubtype` — which walk `datumKindCount` and
  `timeSubtypeCount`, and are the only enforcement of the Datum kind space as a
  closed set — get PG-format counterparts **in this slice**, before any producer
  exists.
- **Watermark invariants.** Property test: for a random tuple and a random access
  sequence, `PackedSlot.Get(i)` equals `MaterializedSlot.Get(i)` for every `i`,
  in any order, including repeats and including a full `Row()` mid-sequence.
- **The escape check.** A test that a partially-deformed slot's `values` cannot
  be observed past `nvalid` — the invariant `codec.go:1327-1330` states and
  04 §2.2 restates.

### MD-04 — the hash join (the measurement)

The floor, plus four numbers, plus the `hashsize` agreement test:

- **Model vs reality.** `hashsize.EntryBytes` predicts the retained bytes per row
  that the executor actually allocates, within a stated tolerance, on a witness
  shape. This test does not exist today for the *current* model either; MD-04 is
  where it becomes necessary, because 04 D-3 changes the model and the storage in
  one commit and nothing else would catch a mismatch (both would be internally
  consistent and both wrong).
- **The batch-count witness.** Q9's build side at PostgreSQL's `work_mem`
  (4 MB × `hash_mem_multiplier` 2). Two pre-states, and the second is the real
  one (04 §0.3): the **modelled** 128 full-width / 64 narrowed
  (`FINDING-p401-alone-is-not-enough.md`, which fed `hashsize.Choose` by hand),
  and the **measured** `Batches: 8 → 1` with narrowed width ≈100 (take3
  `09-verification-and-acceptance.md:48,190`) plus the equal-cardinality A/B in
  `.ralph/deferral_ledger.md:2036` (widths 1098 B vs PG 23 B, 8 batches vs 1,
  63.8 s vs 6.2 s). Report the post-state against the **measured** pair.

  **This witness names a single query, which take3 EX-P2 forbids** (*"gates name
  operators, never queries; any item whose gate names a single query is split or
  dropped"*). MD-04 must either restate the gate over the hash-join operator on a
  named class of shapes, or be split. Q9 may be the *reported example*; it may
  not be the gate.

### MD-05…MD-12 — conversion slices

Floor, plus per slice:

- an alloc arm (allocation count and `inuse_space`, never `alloc_space` for
  retained-heap comparisons), which can fail the slice independently of timing;
- for MD-04 and MD-09, the parallel arm: serial control plus worker arms, with
  the Datum-safety pattern of `parallel_substrate_test.go:26-80`;
- for MD-05, the comparator warning at `operators.go:898-900` — a chunk sorted by
  one comparator and merged by another emits out-of-order rows **with no error**,
  so sort's gate checks ordering explicitly, not just membership.

### MD-1x — conditional alignment (03 D1)

This one changes the **on-disk** format and needs its own gates:

- **Byte goldens against live PostgreSQL 18.3.** For a table covering every
  `typalign` class and both varlena header widths, goopg's tuple bytes equal
  PostgreSQL's, byte for byte, extending the pattern of
  `canonical_tuple_bytes_test.go:39-60` (which already pins `'bootstrap'` →
  `0x15` + 9 bytes, hoff 24). **TOASTed columns are excluded and the test says
  why** — 03 §4, D2 is out of scope, and a golden that quietly skipped them would
  overstate the fidelity claim.
- **Backward read.** Tuples written by the *old* nominal-aligned encoder still
  decode correctly under the new peeking decoder. 01 §3.1 argues they must (a
  zero byte is always safe to align on); this test is what turns the argument
  into a fact.
- **Forward read.** goopg decodes a PostgreSQL-authored tuple containing an
  unaligned short varlena — the case it cannot read today (03 §3.1(b)).
- The PG-standby E2E, if one is reachable, since that is the scenario D1's
  read direction exists for.

### MD-last — `spill.go`

- The private codec's nine test functions (`spill_test.go`,
  `spill_datum_contract_test.go`, 423 test LOC) are **re-pointed at the new
  codec, not deleted**. Retiring `encodeDatum` while retiring its tests is how
  R-5 recurs.
- Round-trip across a real spill on a spilling shape, not a synthetic buffer.

---

## 4. Measurement protocol

Inherited from take3 13 §2.3, restated only where this bundle adds a constraint.

- **Hold server age constant in any A/B.** A goopg server that has just run a
  timeout query sits at `GOMEMLIMIT` with `GOGC=off` and thrashes — "sweep-tail
  collapse" mimics a regression exactly.
- **Always through the cgroup cap:**
  `GOOPG_CG_UNIT=<name> scripts/goopg-test-run.sh ./bin/goopg start -D <dir> --listen 127.0.0.1:<port>`.
  Ports 5533/5534 for throwaway runs; the 6543x block is allocated per
  `CLAUDE.md`. Never `pkill -f goopg` — it self-matches the invoking shell.
- **State `work_mem` and `hash_mem_multiplier` per arm.** goopg's boot default is
  512 MB against PostgreSQL's configured 64 MB on the TPC-H cluster; a
  memory-representation result reported without the budget is unreadable.
- **`timeout N psql` kills only the client** — the server keeps executing. Reap
  orphans between arms, and materialize the victim set before
  `pg_terminate_backend` (`WITH victims AS MATERIALIZED (…)`).
- **Single-run noise is ±17 %** on this workstation, measured from a
  proven-identical-plan pair. A timing claim inside that band is not a claim.
- **Report allocation and retained bytes as first-class results**, not as a
  footnote to a time. This bundle's thesis is about bytes; a slice that wins time
  by allocating more has not done what it says.

---

## 5. Acceptance

The bundle is complete when all of:

1. **Values are unchanged** on both suites, every commit, all the way through —
   ordered and unordered digests plus column signature.
2. **One retention format.** Tier A, Tier B's eight significant sites, and
   `spill.go` all hold packed tuples; no `map[K][]Row` or `[]Row` field remains
   in a `work_mem`-bounded or unbounded-buffer position. R-4 closed by
   construction rather than by discipline.
3. **The model matches the storage.** `hashsize` predicts what the executor
   allocates, with a test.
4. **The batch-count witness moved**, and by how much is recorded next to the
   128/64 pre-state.
5. **`Datum` is still 48 bytes** — `const _ uintptr = 48 - unsafe.Sizeof(Datum{})`
   (`datum.go:187`) untouched. This is the bundle's own falsifiable claim: it did
   not become the change it declined (04 §0.1).
6. **Byte-format fidelity** stated honestly: D1 closed with goldens, D2 recorded
   as an open gap with a ledger row, D3/D4 recorded as non-issues.

**What acceptance does not include.** A time target. 04's thesis is that retained
bytes per row fall; whether that converts to wall time depends on the query, and
R-3 (05 §6) allows the answer to be "no" for narrow rows. Promising a time bar
here would be the mistake take2 07 §6 and take3 13 §0.1 both warn about — plan
work was oversold on time once already in these bundles, and this is executor
work with the same temptation.

---

## 6. Sibling pairs — the standing audit

Every commit checks its pair. In this bundle they are:

| pair | why |
|---|---|
| encode ↔ decode | the classic; and 03's D1 must change both or the format is unreadable in one direction |
| serial hash build ↔ parallel hash build | `operators_join_agg.go:53,71` ↔ `parallel_hash_build.go:43,51`; 04 §4.1 requires one commit |
| `hashsize` model ↔ executor storage | 04 D-3; a mismatch is invisible to a batch-count gate |
| `hashsize.DatumBytes` ↔ `estimatedRowBytes` (`spill.go:541`) | `hashsize.go:~45` already says these must not drift |
| `colTypeInfo` descriptor ↔ `userTypeAttrsForOID` | 03 §5; two transcriptions of one `pg_type.dat` table |
| interpreted `evalExprSlot` ↔ compiled `evalFastExpr` | enforced by `expr_sibling_parity_test.go`; both gain a `*PackedSlot` arm |
| `MaterializedSlot` ↔ `PackedSlot` | every `TupleSlot` method must agree, including `TID()` (04 §7) |

(End of file)
