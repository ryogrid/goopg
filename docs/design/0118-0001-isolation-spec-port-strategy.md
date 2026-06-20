# 0118-0001 — Isolation spec port strategy: runner, normalization, promotion workflow

**Status:** accepted (initial slice)
**Milestone:** [M0118 — Upstream Isolation Spec Suite Pass-Through](../milestones/0118-isolation-spec-suite-passthrough.md)
**Date:** 2026-06-20

## Purpose

This doc anchors *how* M0118 ports upstream PostgreSQL isolation specs to passing
Go tests: how the runner executes a `.spec`, how its output is normalized to the
`expected/*.out` oracle, and the per-spec promotion workflow. It also records the
two runner/engine fixes that the first slice (M0118-0001) required to land
`simple-write-skew`.

## Runner overview

`internal/testport/framework/isolation.go` parses a `.spec` into an
`IsolationSpec` (global/per-session setup+teardown, declared steps, and any
explicit `permutation` lines). `isolation_runner.go` then runs each permutation
against a live goopg cluster and formats output to mirror upstream
`isolationtester.c` (the `Parsed test spec with N sessions` banner, per-step
echo, `pqprint`-style result tables, `<waiting ...>`/`<... completed>` blocking
markers, and `ERROR:` lines). `RunAndCompare` normalizes both sides
(`normalizeIsoOutput`) and diffs them; a byte-identical match is `pass`.

A spec is ported by a dedicated, **sequential** `TestPort_Isolation<Name>`
function (own cluster, no `t.Parallel()`) calling `runIsoSpec`. The shared
`TestPort_IsolationSuite` runs every spec in parallel for coarse observability,
but its per-spec subtests share one cluster whose lifecycle is governed by a
parent `defer c.Stop(...)`; a single filtered parallel subtest can therefore see
the cluster already stopped (a Go parallel-subtest/defer ordering artifact). The
dedicated per-spec functions are the authoritative pass evidence.

## Promotion workflow (every slice)

1. Make the spec green via its dedicated `TestPort_Isolation<Name>` test.
2. Set its row in `docs/test-port/postgres-oracle-target-inventory.csv` to
   `status=pass`, rationale = the Go test function name + what changed.
   (Rationale fields are comma-free — use `;`/`—` — because the CSV is
   unquoted.)
3. Regenerate the derived docs:
   - `go run ./cmd/gen-isolation-coverage --repo-root .`
   - `go run ./cmd/gen-oracle-inventory --repo-root .`
   - `go run ./cmd/gen-oracle-port-status` (D-002 suite-level rationale).
4. Keep the already-passing specs green; run the pgbench pre-commit smoke.

## Slice M0118-0001 fixes

### 1. Auto-generate all permutations (`run_all_permutations` parity)

Most SSI anomaly specs (`simple-write-skew`, `two-ids`, `total-cash`,
`receipt-report`, `project-manager`, `classroom-scheduling`, …) declare **no**
explicit `permutation` lines. Upstream `isolationtester.c run_all_permutations`
then runs *every* interleaving of the per-session step sequences (each session's
steps kept in declaration order). The goopg runner previously left
`IsolationSpec.Permutations` empty in that case, so it ran zero permutations and
mis-reported every declared step as `unused step name: …` — an instant,
spurious `defer`.

`isolation.go generateAllPermutations` now fills `Permutations` when a spec
declares none, mirroring `run_all_permutations_recurse`: at each position it
picks the next unconsumed step from each session in session-declaration order and
recurses, emitting a permutation when all per-session piles are exhausted. The
emitted order is byte-identical to upstream (verified: the 6 interleavings of
`simple-write-skew`'s two 2-step sessions match `expected/simple-write-skew.out`
exactly). Auto-generated permutations carry no `StepBlocker`s, so
`PermutationBlockers` is left short of `Permutations`; `RunSpec` already treats a
missing entry as nil.

### 2. SSI `40001` wire-message parity (clean errmsg + DETAIL)

`predicate.c` raises serialization failures as:

```
errmsg("could not serialize access due to read/write dependencies among transactions")
errdetail_internal("Reason code: <reason>.")
errhint("The transaction might succeed if retried.")
```

`isolationtester` (and psql's `ERROR:` line) print only the **primary** errmsg;
the reason code is a separate DETAIL it suppresses. goopg's commit-path SSI abort
previously built the wire message as
`"<errmsg>: " + SerializationFailureError.Error()`, and `Error()` itself appends
the reason in parentheses — yielding a doubled, reason-inlined primary message
that never matched the oracle.

`mvcc.SerializationFailureError` now exposes `PrimaryMessage()` (the bare
errmsg, no reason) and `Detail()` (`"Reason code: <reason>."`). Both wire seams —
`executor/ssi.go ssiPreCommitCheck` (`*ExecError`) and
`server/dispatch.go` (explicit-COMMIT path, `writeQueryError` + `FieldDetail`/
`FieldHint`) — now emit the bare primary message and carry the reason as DETAIL
and the retry hint as HINT, matching `operators_storage.go`'s existing
during-write site. `Error()` is unchanged (its parenthesised reason remains for
Go-side logging only). The focused `ssi_write_skew_test.go` assertions
(`strings.Contains(... "could not serialize access due to read/write
dependencies among transactions")`) still hold.

### 3. Committed-xact retention + `OnConflict` at edge creation (lands `two-ids`)

The 2-cycle write-skew that `simple-write-skew` exercises is fully caught by the
commit-time `PreCommitCheckForSerializationFailure` walk. The **read-only
anomaly** in `two-ids` is not: its dangerous structure `s3 →rw→ s2 →rw→ s1` has
`s1` (the pivot's out-conflict partner) **already committed** by the time the
closing `s3 →rw→ s2` edge is drawn. Before this slice goopg scrubbed a committed
xact's edges and deleted it at `finish`, so the `s2 →rw→ s1` edge to committed
`s1` was gone — a false negative.

Three coordinated `mvcc` changes (no executor/hot-path change) close it,
mirroring `predicate.c`:

- **Retention.** `releaseSerializableLocked(handle, committed)` no longer scrubs
  a *committed* xact. It stamps `FinishedAt`, sets the `ConflictOut` flag
  (`SXACT_FLAG_CONFLICT_OUT`) if the xact holds an out-conflict to an
  already-committed peer, and moves it to a new `ssiState.finished` pointer slice
  with its rw-edges intact. Aborted xacts are scrubbed + dropped as before
  (they cannot be part of a *committed* dangerous structure). `xacts` stays
  active-only, so proc-slot handle reuse is safe; edges address peers by
  `*SerializableXact`, so the retained slice has no handle-aliasing hazard.
  `serializableXactByXIDLocked` scans `finished` too, so a read of a committed
  writer's tuple still finds the writer.
- **Overlap-scoped purge.** Each xact gets a `BeginAt` stamp from the same dense
  counter as `FinishedAt`. `purgeFinishedSerializableLocked` (run at register and
  at finish) drops a retained xact `C` once no active xact began before `C`
  finished (`minActiveBegin ≥ C.FinishedAt`) — goopg's analogue of the
  `SxactGlobalXmin`-driven cleanup. This bounds retention to the concurrency
  window and clears prior-permutation state before handle reuse.
- **`onConflictCheckLocked`** (port of `OnConflict_CheckForSerializationFailure`)
  runs the moment a new edge `reader → writer` is recorded, on both the read and
  write hooks. It checks the three upstream structures (Case 1 committed writer
  with `ConflictOut`; Case 2 writer-pivot with a committed out-conflict `T2`;
  Case 3 reader-pivot with a committed writer) and **dooms** the transaction
  upstream would cancel. `two-ids` is entirely Case 2 (doom the in-flight pivot,
  deferred to its own `PreCommit`), matching PG's all-at-COMMIT output exactly.

**`XidIsConcurrent` gate (load-bearing once retention is on).** Retaining
committed writers exposed a latent phantom: the read-path hook fires for the
*creator* (`xmin`) of the version the reader sees, and a reader that already sees
the writer's commit (writer committed before the reader's snapshot) must **not**
form an anti-dependency — it is an ordinary read. `Snapshot.XidIsConcurrent`
(mirroring `predicate.c`'s `XidIsConcurrent`) now gates
`checkForSerializableConflictOutLocked` using the reader's pinned `firstSnap`;
without it, `two-ids`' first permutation (`wx1 c1 rxwy2 …`, where `s2` reads
committed `D1`) raised a spurious `40001`.

**Deferral vs upstream (documented, conservative).** goopg does not yet model
`READ ONLY` transactions or two-phase `PREPARE`; every xact is treated READ WRITE
and a finished xact is treated as prepared+committed (`FinishedAt` doubles as
`prepareSeqNo`/`commitSeqNo`). The read-path reader-kill is now surfaced
mid-statement (section 4); the write-path pivot-writer doom (`current == writer`)
is deliberately kept deferred to the writer's own `PreCommit` — no spec needs the
write-path mid-statement abort, and `read-write-unique`'s mid-statement `w2`
`40001` comes from the unique-index conflict path, not this hook.

### 4. Mid-statement read-path abort + index-UPDATE conflict-in (lands `total-cash`)

`total-cash` is the read-only-anomaly variant whose abort PG raises **during a
`SELECT`** (e.g. `rxy2`), not at COMMIT: when the in-flight reader closes a
dangerous structure to an already-**committed** writer it cannot abort, upstream
`ereport`s in place (`predicate.c`: the entry `SxactIsDoomed(MySerializableXact)`
check, and `OnConflict`'s "Canceled on conflict out to pivot, during read"). Two
coordinated changes land it:

- **Surface the reader-kill mid-statement.** `reader.Doomed` is set *only* by the
  read-path reader-kill arm of `onConflictCheckLocked` (the write path dooms the
  *writer*; `PreCommit` dooms the pivot at commit), so it is an exact predicate
  for "this read must abort now". New `mvcc.Manager.CheckForSerializableConflictOutReportingFailure`
  wraps the conflict-out check and returns a `*SerializationFailureError` when the
  reader is doomed at entry or becomes doomed across the call. The executor hook
  `ssiRecordTupleRead` now returns that error, and the **three** SERIALIZABLE read
  sites — `seqScanOp.Next`, `indexScanOp.Next`, and the UPDATE/DELETE
  `scanMatching` loop — propagate it (releasing any held page RLock first). The
  statement aborts in place, marking the transaction failed (25P02) so the
  following `COMMIT` is a silent rollback, exactly as PG/isolationtester print.
  Deferred dooms (the writer is the victim, the `two-ids` / `simple-write-skew`
  shape) leave `reader.Doomed` clear → the hook returns `nil` and the reader keeps
  running, surfacing at the writer's COMMIT as before.
- **Sibling-path fix: index-based UPDATE conflict-in.** `total-cash`'s
  `wy2`-before-`rxy1` permutations also exposed a pre-existing gap: the
  index-driven UPDATE path (`updateViaIndex`, used for `WHERE accountid = …` on a
  PRIMARY KEY) never called `ssiRecordTupleWrite`, so a SERIALIZABLE UPDATE found
  via an index installed **no** write-path conflict-in edge from concurrent SIREAD
  holders — the seqscan UPDATE path (`scanMatching` + `ssiRecordTupleWrite`) did.
  Without the `s1 → s2` edge the reader-pivot 2-cycle was never formed and the
  reader-kill could not fire. `updateViaIndex` now records the conflict-in on the
  old tuple's `(rel, blk, slot)` after each write, mirroring the seqscan path. The
  change is a no-op for RC/RR (the `ssiActive` guard), so only SERIALIZABLE UPDATEs
  are affected; the full UPDATE/MERGE/conflict isolation suite stays green.

### 5. Phantom predicate locking (lands `project-manager`)

`project-manager` is a 2-cycle whose second edge is a **phantom**: `s2` runs
`SELECT count(*) FROM project WHERE project_manager = 1` over an (initially empty)
table and `s1` later `INSERT`s a row that *would have matched*. The per-tuple
SIREAD locks goopg took only cover rows that already exist, so the insert formed
no rw-conflict — a false negative. PG closes this with relation-grain predicate
locking plus a conflict-out on the physical insert. Four coordinated changes land
all 21 permutations byte-identical:

- **Relation-level SIREAD on seq scans.** `seqScanOp.Open` now acquires a
  relation-grain predicate lock on the scanned table — the analogue of
  `PredicateLockRelation` in upstream `heap_beginscan` (`heapam.c:1162`): *"for
  seqscan … acquire a predicate lock on the entire relation … to conflict with
  new insertions into the table … in a heap scan there is nothing more
  fine-grained to lock."* New executor hook `ssiRecordRelationRead`; gated to skip
  system catalogs (`catalog.IsSystemRelation`), temp and matview relations exactly
  as upstream `PredicateLockingNeededForRelation`. This handles the
  READ-before-INSERT ordering: the later insert's conflict-in walk finds the
  reader's relation lock.
- **Conflict-out on physically-present invisible tuples.** For the
  INSERT-before-READ ordering, `s1`'s uncommitted insert is physically on the page
  but invisible to `s2`. Upstream `HeapCheckForSerializableConflictOut`
  (`heapam.c:9254`) runs for *every* tuple a scan examines, visible or not, and on
  the `!visible` paths registers `reader → inserter` from the tuple's xmin. The
  `seqScanOp.Next` invisible branch now calls new hook
  `ssiRecordInvisibleTupleRead(rel, xmin)`; the Manager filters
  aborted/bootstrap/frozen/own-snapshot xids so only a genuine concurrent inserter
  forms the edge.
- **Retained committed readers' predicate locks.** The 6 permutations where one
  session COMMITs before the other's conflicting write needed the *committed*
  reader's SIREAD lock to remain discoverable by the later write's conflict-in.
  `releaseSerializableLocked` now, on **commit**, detaches the xact only from the
  GLOBAL (handle-keyed) holder sets — which a reused proc-slot handle could alias —
  but **keeps** `sx.predicateLocks` (`detachPredicateLocksFromGlobalLocked`).
  `checkForSerializableConflictInLocked` gains a second walk over
  `ssiState.finished`, forming `R → W` against any retained committed reader that
  (a) overlaps the writer (`reader.FinishedAt > writer.BeginAt`) and (b) owns a
  covering tag. Mirrors PG keeping a committed `SERIALIZABLEXACT`'s predicate
  locks alive until `purgeFinishedSerializableLocked` drops it.
- **Write-path mid-statement abort.** When the write closes a dangerous structure
  in which the writer is the pivot with an out-conflict to an already-committed
  partner, PG `ereport`s *during the write* (`predicate.c:4667`, "Canceled on
  identification as a pivot, during write."). New
  `mvcc.Manager.CheckForSerializableConflictInReportingFailure` returns a
  `*SerializationFailureError` when the writer becomes newly doomed across the
  check; `ssiRecordTupleWrite` now returns that error and **all seven** write
  sites (insert ×2, update-via-index, update-via-seqscan, delete, MERGE
  update/delete) propagate it so the statement fails in place (25P02). Deferred
  pivot dooms (partner still in flight) leave the flag clear and surface at the
  writer's own COMMIT, exactly as before — the `simple-write-skew` shape.

All four changes are `ssiActive`-gated, so RC/RR and non-SERIALIZABLE workloads
(pgbench, TPC-H) are byte-for-byte unchanged; the full UPDATE/MERGE/insert-conflict
isolation suite stays green.

### 6. Comma-separated `BEGIN` modes + `read-write-unique` family (this slice)

No SSI engine change this slice — the machinery from sections 2–5 already produces
the upstream results for the unique-constraint write-skew family. Two things landed:

- **Parser: comma-separated transaction modes.** Upstream specs write
  `BEGIN ISOLATION LEVEL SERIALIZABLE, READ ONLY` (receipt-report, read-only-anomaly*)
  but `parseBeginModes` stopped at the comma (`default → goto done`), leaving
  `, READ ONLY` as a dangling token → *syntax error at the comma column*. The mode
  loop now consumes an optional `,` between modes (`_ = p.acceptSymbol(",")`),
  matching PG's `transaction_mode_list` grammar (`gram.y`) and the sibling fix
  already present in `SET TRANSACTION`. Covered by
  `TestParseBeginCommaSeparatedModes`. (A separate, pre-existing gap remains: a
  bare `DEFERRABLE` keyword after `READ ONLY` is tokenized as a reserved keyword,
  not an ident-keyword, so `parseBeginModes`' `acceptIdentKeyword("deferrable")`
  misses it — needed only by `read-only-anomaly-3`, deferred.)
- **`read-write-unique`, `-2`, `-3` promoted to `pass`.** All three now match
  PG 18.3 byte-for-byte: the SIREAD predicate locks on the `i = 42` / key probe
  plus the write-path conflict-in (sections 2–4) yield `40001` on the overlapping
  interleavings and a plain `23505` unique violation on the serialized ones.
  `-3` exercises the same shape through a `LANGUAGE SQL` insert-if-not-exists
  function (bug 9301). Ported as `TestPort_IsolationReadWriteUnique{,2,3}`.

### 7. Index-scan SSI completeness + `40001`-vs-`23505` ordering (lands `read-write-unique-4`)

`read-write-unique-4` implements a gapless per-year invoice sequence and mixes both
outcomes in one spec:

- `r1 r2 w1 w2 c1 c2` — **both** sessions probe `MAX(invoice_number) … WHERE year = 2016`
  first, so the loser's INSERT closes a dangerous structure → `40001`.
- `r1 w1 w2 c1 c2` and `r2 w1 w2 c1 c2` — only **one** session probes, so there is no
  pivot and the duplicate is a plain `23505`.

The earlier blanket rule in `uniqueCheckWithWait` raised `40001` for *every* post-wait
unique conflict between two SERIALIZABLE writers. That happened to match the
both-read specs (`read-write-unique{,-2,-3}`) but over-fired the single-reader
permutations here. The real discriminator is whether the writer is an SSI pivot,
which only the conflict graph knows. Two coupled fixes:

- **Defer the `40001`-vs-`23505` decision to the SSI conflict-in walk.**
  `uniqueCheckWithWait` no longer hard-codes `40001` for SERIALIZABLE. After the
  in-flight inserter commits and the duplicate survives, it runs
  `ssiRecordTupleWrite` against the **committed conflicting tuple's** location
  (`tuple → page → relation` holders, including retained-committed readers). A
  non-nil result means this writer is a pivot to an already-committed peer → `40001`;
  otherwise the duplicate falls through to `23505`. This mirrors upstream's
  `_bt_check_unique` / `CheckForSerializableConflictIn` ordering. (REPEATABLE READ
  keeps its prior `40001`; the third-in-flight-xact corner with no committed
  location to walk keeps the prior conservative `40001`.)

- **Close the SERIALIZABLE index-scan / index-only-scan predicate-lock gaps.** The
  conflict-in walk only finds a reader if the reader registered a phantom-covering
  predicate lock — but `WHERE year = 2016` / `WHERE i = 42` / `WHERE key = k` are
  served by `indexScanOp` / `indexOnlyScanOp`, neither of which took the
  relation-grain SIREAD that `seqScanOp` does (section 5), nor the invisible-tuple
  conflict-out (the INSERT-before-READ phantom). Both operators now:
  1. acquire a **relation-grain SIREAD on the heap relation** at `Open` (held even
     when the probe matches no key — that empty-result gap *is* the phantom), and
  2. register the **invisible-tuple conflict-out** (`ssiRecordInvisibleTupleRead`)
     when an index entry points at a tuple invisible because a concurrent xact
     inserted it — the analog of `seqScanOp.Next`'s invisible branch. This is what
     `read-write-unique-3` needs: each `LANGUAGE SQL` `insert_unique` call reads the
     key (finding the peer's in-flight insert invisible) then inserts the duplicate
     itself; without the conflict-out the loser's out-edge never forms and it is not
     a pivot.

These are general correctness fixes (goopg's index access paths were SSI-incomplete
relative to seq scans), not spec-specific patches. Upstream locks index *leaf pages*
(`PredicateLockPage`); goopg's write-path conflict-in walk is keyed on the heap
relation, so the relation-grain SIREAD is the faithful walk-reachable analog —
slightly coarser than PG (more conservative, never a false negative). All four
`read-write-unique{,-2,-3,-4}` specs now pass via the real machinery, with the
blanket-`40001` heuristic removed. Ported as `TestPort_IsolationReadWriteUnique4`.

> Latent (not needed by any in-scope spec, noted for follow-up): `indexOnlyScanOp`
> still does not register a per-tuple SIREAD + conflict-out for *visible* rows it
> reads (only the relation-grain phantom lock and the invisible-tuple conflict-out).
> A read-then-write-skew driven purely through an index-only scan over existing rows
> would miss its read edge. No current spec exercises that path.

### 8. Zero-engine-change promotions + the predicate-lock-granularity boundary

Two more specs were promoted this slice with **no engine change** — they already
matched PG 18.3 against the machinery built in sections 3–7:

- **`read-only-anomaly`** (`TestPort_IsolationReadOnlyAnomaly`). This is the O'Neil
  read-only anomaly run under **REPEATABLE READ** (snapshot isolation), so the
  anomaly is *allowed*: no serialization failure occurs and the read-only `s3`
  observes the inconsistent `(X=0, Y=20)` state. No SSI is involved at all; goopg's
  RR snapshot semantics reproduce the expected output byte-for-byte. (Contrast its
  `-2` / `-3` siblings, which raise the level to SERIALIZABLE — see below.)
- **`update-conflict-out`** (`TestPort_IsolationUpdateConflictOut`). Exercises SSI
  conflict-out handling for heapam when a `trouble` session adds a second physical
  version (UPDATE) or removes one (DELETE) and then aborts (bug `db7b729d`). Both
  permutations abort session `bar` with `40001` at `bar_commit` where PG does. This
  passes on the retained-committed-xact conflict graph (section 3) plus the
  index-UPDATE write-path conflict-in edge (section 4) already in place.

**Boundary surfaced — predicate-lock granularity (deferred to M0118-0002).**
`read-only-anomaly-2` (the SERIALIZABLE sibling) is **blocked**, and the diagnosis
draws the scope line for the rest of the M0118-0001 family. Its permutation 2
(`… s3r s3c s2wx`) already matches: the read-only `s3` observes `s1`'s committed
write, closing the real cycle, and `s2wx` aborts with `40001`. But permutation 1
(`s2rx s2ry s1ry s1wy s1c s2wx s2c s3c`) **over-aborts** `s2wx` with a false-positive
`40001` where PG commits both `s1` and `s2`. Root cause:

- `s1`'s `SELECT … WHERE id = 'Y'` takes a **relation-grain SIREAD** on
  `bank_account` (the coarse phantom lock from sections 5/7 — faithful but
  over-approximate). That lock phantom-covers `X` even though `s1` only read `Y`.
- When `s2` writes `X` (`s2wx`), `checkForSerializableConflictInLocked` finds `s1`
  (retained committed) as a covering holder and installs a spurious `s1 → s2` rw-edge.
- Combined with the *real* `s2 → s1` edge (`s2` read `Y` before `s1` wrote it), `s2`
  now looks like a pivot in a 2-cycle, so `onConflictCheckLocked` Case 2 dooms it.

PG avoids this because `s1`'s index-qualified read locks only the `Y` index
leaf / tuple, not the whole relation, so no `s1 → X` edge ever forms. Closing this
needs **finer predicate-lock granularity per access method** (tuple/page-grain SIREAD
on index equality probes) — precisely the M0118-0002 charter. It is deliberately
*not* attempted here: the relation-grain phantom is load-bearing for the specs that
*do* pass (`read-write-unique-3`'s empty-probe phantom, `project-manager` /
`classroom-scheduling`'s dangerous-structure closure), so narrowing it is a
cross-cutting change that must land with its own regression budget. The same
over-abort gap explains `receipt-report`'s 48-vs-6 serialization-failure surplus and
the `serializable-parallel` family. `TestPort_IsolationReadOnlyAnomaly2` is kept as a
skip-until-green baseline anchor so the fix flips it to PASS automatically.

### 9. Single-column full-key index-scan SIREAD granularity (lands `read-only-anomaly-2`; `multiple-row-versions` SSI-correct)

Section 8 deferred predicate-lock granularity wholesale to M0118-0002. This slice
takes the first, **surgical** bite of it — the one that needs no new locking
mechanism, only a narrower choice of *which* SIREAD to take — and it flips two
specs.

**The over-abort, restated via `multiple-row-versions`.** That spec's single
tested permutation (`rx1 wx2 c2 wx3 ry3 wy4 rz4 c4 c3 wz1 c1`) is a 4-xact
dangerous structure where PG aborts **only** `wz1` (s1) with `40001`; `c3` (s3)
commits cleanly. goopg instead *also* aborted s3 at `c4`. Trace: `s1 rx1` reads
`id=1000000` version **v0**; `s2 wx2` overwrites it (→ v1, commits); `s3 wx3`
overwrites **v1** (→ v2). The real antidependency is `s1→s2` (s2 obsoleted the
version s1 read). But goopg's index scan took a **relation-grain** SIREAD in
`openPrep`, covering *every* version of `id=1000000`, so `s3`'s write to v1's slot
spuriously matched s1's lock and installed a phantom **`s1→s3`** edge. With both
`s1→s3` and `s3→s4` present, `PreCommit_CheckForSerializationFailure` for `c4`
walked s4→pivot s3→in-flight s1 and doomed s3. PG never forms `s1→s3` because s1's
heap-tuple SIREAD is on v0's TID only, and s3 overwrote v1.

**Fix (`operators_index.go`).** The eager relation-grain SIREAD is removed from
`openPrep`; the granularity decision is deferred to the end of `Rescan` once the
matched TID set is known (`ssiRecordIndexScanGapLock`):

- A **single-column full-key point lookup that matched ≥1 tuple** relies on the
  exact per-tuple SIREAD locks already recorded in `Next` (each on the precise
  `(block, slot)` the reader observed). No relation lock → no coverage of versions
  the reader never read → no phantom `s1→s3`.
- **Everything else keeps the relation-grain gap lock**: empty probes (the
  `read-write-unique` phantom gap — no tuple read means no per-tuple lock exists),
  **leading-column probes on a composite index** (e.g. `read-write-unique-4`'s
  `WHERE year = 2016` on PK `(year, invoice_number)` — a range over the trailing
  column with a real gap a concurrent `(2016, N)` INSERT falls into), and range
  scans.

The full-key/single-column restriction is what makes this safe: classic write-skew
(`two-ids`, `simple-write-skew`) has the writer overwrite the **same version** the
reader read, so tuple-grain still catches it; only the *intervening-version* case
diverges. The full passing family stays byte-for-byte green.

**Outcome.** `read-only-anomaly-2` → **PASS** (`TestPort_IsolationReadOnlyAnomaly2`;
the section-8 phantom `s1→s2` edge is gone). `multiple-row-versions` is now
**SSI-correct** — `TestPort_IsolationMultipleRowVersions` matches PG byte-for-byte
when its setup succeeds — but is **not yet `pass_required`**: the spec setup
`INSERT … generate_series(1, 1000000)` in one transaction intermittently trips the
orthogonal WAL-buffer ring race `errWALBufferReservedOutOfRange`
(`internal/wal/wal_buffer.go`), failing ~50% of runs *before any step executes*.
That is a separate subsystem; the test is kept as a skip-on-defer anchor that
auto-promotes once the bulk-insert WAL race is fixed. The `receipt-report` part-2
and `serializable-parallel` over-aborts that section 8 attributed to the *same*
relation-grain gap may now also improve, but they carry additional blockers
(READ ONLY SSI modeling, `RO_SAFE`) and are not promoted here.

### 10. Parenthesised query source for `INSERT` (lands `partial-index`)

`partial-index` exercises SSI predicate locking through a *partial* index:
`CREATE INDEX test_idx ON test_t(id) WHERE val2 = 1`, two SERIALIZABLE xacts
each `SELECT * FROM test_t WHERE val2 = 1` and then `UPDATE … SET val2 = 2`
(moving a row *out* of the partial index). Any overlap must raise `40001`.

The SSI machinery needed **zero** change: the section-9 index-scan SIREAD
granularity already records per-tuple read locks for the `WHERE val2 = 1`
predicate read, and the concurrent `UPDATE`s that move rows out of the index
install the cross-covering rw-edges that close the dangerous structure — exactly
as for a full index. All generated permutations match PG 18.3 byte-for-byte
(`TestPort_IsolationPartialIndex`, stable 3/3 at ~23s).

The **sole** blocker was a parser gap. The spec's `global setup` seeds the table
with `insert into test_t (select generate_series(0, 10000), 'a', 2);` — a fully
**parenthesised query source**. PostgreSQL's `insert_rest` grammar permits a
`SelectStmt` (which includes `select_with_parens`) as the source, so the leading
`(` here opens a query, **not** a column list. goopg's `parseInsert`
(`internal/parser/dml.go`) unconditionally consumed a `(` as the start of a
column-name list and then failed at the `select` keyword (`42601`).

Fix: a `nextIsParenQuerySource()` peek decides the `(`. It scans the run of
leading `(` and reports whether the first non-`(` token is a query-starting
**reserved** keyword (`SELECT` / `VALUES` / `WITH` / `TABLE`) — none of which can
be a bare column name, which is what makes the lookahead unambiguous. When true,
the source is parsed via the existing `parseParenthesisedSelectStmt` (reused from
set-op RHS handling, so nested `((SELECT …))` works); otherwise the `(` is a
column list as before. The check is applied both for a bare source
(`INSERT INTO t (SELECT …)`) and after an explicit column list
(`INSERT INTO t (a, b) (SELECT …)`, also valid upstream). The change is strictly
additive — only input that previously raised a syntax error now parses — so no
existing `INSERT` parse is altered (regression tests:
`TestParseInsertParenthesisedSelectSource`,
`TestParseInsertColumnListThenParenthesisedSelect`,
`TestParseInsertPlainSelectSourceUnchanged`).

### 11. `date` B-tree key type (lands `temporal-range-integrity`)

`temporal-range-integrity` is a SERIALIZABLE temporal write skew across two
tables: `s1` reads `statute` with a date-range predicate
(`eff_date <= DATE '…' AND (exp_date IS NULL OR exp_date > DATE '…')`) then
`INSERT`s into `offense`; `s2` reads `offense` with a date-range predicate then
`DELETE`s from `statute`. Any overlap must raise `40001`. The expected output is
**not** uniform — permutations where `s1` fully commits before `s2`'s read see
the inserted row and serialize cleanly, while permutations where `s2`'s read
misses `s1`'s uncommitted insert close the two-rw-edge dangerous structure and
abort.

The SSI machinery needed **zero** change. The deleted `statute` row and the
inserted `offense` row both fall *within* the concurrent readers' predicates, so
even relation-grain SIREAD locks cross-cover exactly the rows PG's finer locks
do — the dangerous-structure detection validated across the prior 23 specs
reproduces the abort pattern (including the empty-relation predicate reads,
which still take a relation-grain predicate lock that the later insert/delete
conflicts with). All generated permutations match PG 18.3 byte-for-byte
(`TestPort_IsolationTemporalRangeIntegrity`, stable 3/3 at ~2.5s).

The **sole** blocker was B-tree key-type support. `statute`'s primary key is
`(statute_cite text, eff_date date)`; `text` is an accepted B-tree key type but
`date` was not, so the spec's `global setup` aborted with `0A000 btree v0 only
supports int4 / numeric keys, got "date"` (`createBTreeIndex`,
`internal/executor/operators_ddl.go`).

Fix: `date` joins the accepted B-tree key types (`isDateType` →
`isSupportedBTreeKeyType`). A PG `date` is `int32` days since the 2000-01-01
epoch, so `encodeBTreeKeyForColumn` encodes it order-preservingly through the
existing `int4` path (`btree.EncodeInt4(days)`) using the **same**
days-since-epoch arithmetic the wire codec uses (`codec.go` `case "date"`), so a
probe key built from a `DATE` literal is byte-identical to the key backfill
produced for the stored row. Both index-only-scan key decoders
(`internal/executor/operators_indexonly.go`) gained the symmetric `date` case
(`DecodeInt4` → days → `time.Time`) so an index-only scan over a `date` key
column round-trips rather than falling through to the float8 default — a latent
sibling-path bug closed in the same change even though this spec does not drive
an index-only scan. Regression tests: `TestDDLCreateDateBTreeIndexAcceptsType`,
`TestDDLDateRangeScanParity` (mirroring the M0044 timestamp tests). This unblocks
`date`-keyed indexes generally (e.g. TPC-H `date` columns), not only this spec.

## Status / scope boundary

- **Passing:** `simple-write-skew` (2-cycle write skew), `two-ids` (3-xact
  read-only anomaly, all 90 generated permutations byte-identical to PG 18.3),
  `total-cash` (mid-statement read-path abort, all 20 permutations — section 4),
  `project-manager` (phantom predicate locking, all 21 permutations
  byte-identical to PG 18.3 — section 5), and the full `read-write-unique` family
  `{base, -2, -3, -4}` (unique-constraint write skew, sections 6–7; `-4` mixes
  `40001` and `23505` across its three permutations via the conflict-in walk + the
  index-scan SSI completeness fixes), `read-only-anomaly` (REPEATABLE READ — anomaly
  allowed, no SSI), `update-conflict-out` (SSI conflict-out vs a concurrently
  UPDATEd/DELETEd-then-aborted tuple; both section 8, zero engine change),
  `read-only-anomaly-2` (single-column full-key index-scan SIREAD granularity —
  section 9), `predicate-lock-hot-tuple` (`IN`-list point reads cross-covering
  each other's UPDATE target — write-skew 2-cycle, zero engine change via the
  section-9 per-tuple SIREAD locks), `partial-index` (SSI through a partial
  index; zero SSI change — only the parenthesised `INSERT … (SELECT …)` parser
  source of section 10), and `temporal-range-integrity` (cross-table temporal
  write skew; zero SSI change — only the `date` B-tree key type of section 11).
- **Still deferred (same slice family):**
  - `classroom-scheduling` — the SSI machinery is in place (section 5), but its
    primary key is `(room_id text, start_time timestamptz)` and goopg's btree
    rejects a `timestamptz` key (`btree v0 only supports int4 / numeric keys`,
    SQLSTATE 0A000) at the spec's `global setup`. Section 11 added the `date` key
    type via the int4-days path; `timestamptz` is a distinct (8-byte
    microsecond-since-epoch, like `timestamp`) follow-on and is the remaining
    blocker. Blocked on btree key-type support, NOT on SSI — a different subsystem.
  - `receipt-report` — the `BEGIN ISOLATION LEVEL SERIALIZABLE, READ ONLY` parser
    gap is now **fixed** (section 6), so the spec runs end-to-end. Two issues
    remain: (a) the spec's `date` columns read back through the isolation runner
    as `2008-12-22T00:00:00Z` because lib/pq decodes the `date` OID into
    `time.Time` and `database/sql`'s NullString scan re-renders it `RFC3339` — a
    runner-fidelity gap, not a goopg wire bug; PG's regress harness also needs
    `DateStyle='Postgres, MDY'` for the expected `12-22-2008`; and (b) de-facto
    READ ONLY SSI modeling, without which goopg produces 48 serialization
    failures where PG produces 6 (the 42 false positives upstream notes for a
    READ WRITE `s3`, because goopg does not yet treat `s3` as read-only in SSI).
  - `multiple-row-versions` — **SSI-correct as of section 9** (output matches PG
    byte-for-byte) but not `pass_required`: its 1,000,000-row single-transaction
    setup INSERT intermittently trips the orthogonal WAL-buffer ring race
    `errWALBufferReservedOutOfRange` (`internal/wal/wal_buffer.go`), ~50% of runs.
    Blocked on that WAL race (a different subsystem), NOT on SSI.
    `TestPort_IsolationMultipleRowVersions` is a skip-on-defer anchor.
  - `read-only-anomaly-3` / the `serializable-parallel` family — still blocked on
    **predicate-lock granularity** beyond the section-9 slice plus their own extra
    blockers. The section-9 single-column full-key narrowing fixed
    `read-only-anomaly-2`; the remaining members need finer per-access-method
    locking (M0118-0002 proper) for their range/aggregate reads. `-3` additionally
    needs the `DEFERRABLE` safe-snapshot deferral and the reserved-keyword parser
    fix (section 6); the `serializable-parallel-2/-3` pair additionally needs the
    `RO_SAFE` read-only-safe-snapshot optimisation.
  Their dedicated `TestPort_Isolation*` functions auto-promote (run, then
  `t.Skip` only on `defer`), so the next slice sees green→pass instantly.
