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

### 12. `timestamptz` B-tree key type (lands `classroom-scheduling`)

`classroom-scheduling` is the canonical SSI double-booking write skew: `s1` reads
`room_reservation` with an overlap predicate
(`start_time < … AND end_time > …`) then `INSERT`s an overlapping booking; `s2`
reads with its own overlap predicate then `UPDATE`s the existing booking's
`start_time` into the contested window. Any overlap between the two transactions
must raise `40001`. Across the generated permutations only the two that fully
serialize `s1` before `s2`'s read commit cleanly; every interleaving that closes
the two-rw-edge dangerous structure aborts.

The SSI machinery needed **zero** change. The inserted and updated rows both fall
*within* the concurrent readers' overlap predicates, so the relation/tuple-grain
SIREAD locks cross-cover exactly the rows PG's finer locks do — the
dangerous-structure detection validated across the prior 24 specs reproduces the
abort pattern. The spec emits only `count` values and `ERROR` lines, so
`timestamptz` text rendering (timezone display) is never exercised by the
comparison. All generated permutations match PG 18.3 byte-for-byte
(`TestPort_IsolationClassroomScheduling`, stable 3/3).

The **sole** blocker was B-tree key-type support. `room_reservation`'s primary
key is `(room_id text, start_time timestamptz)`; `text` is an accepted B-tree key
type but `timestamptz` was not, so the spec's `global setup` aborted with `0A000
btree v0 only supports int4 / numeric keys` (`createBTreeIndex`,
`internal/executor/operators_ddl.go`).

Fix: `timestamptz` joins the accepted B-tree key types (`isTimestamptzType` →
`isSupportedBTreeKeyType`). PG stores both `timestamp without time zone` and
`timestamp with time zone` as `int64` microseconds since the 2000-01-01 epoch
(timestamptz normalized to UTC) — byte-for-byte the **same** on-disk form — so
`encodeBTreeKeyForColumn` routes `timestamptz` through the existing `timestamp`
key path (`btree.EncodeTimestamp(micros)`). Because the same `KindTime` Datum
representation feeds both backfill and probe, a probe key built from a
`TIMESTAMP WITH TIME ZONE` literal is byte-identical to the key backfill produced
for the stored row. Both index-only-scan key decoders
(`internal/executor/operators_indexonly.go`) gained the symmetric `timestamptz`
case alongside `timestamp` — a latent sibling-path bug closed in the same change
even though this spec does not drive an index-only scan. Regression tests:
`TestDDLCreateTimestamptzBTreeIndexAcceptsType`,
`TestDDLTimestamptzRangeScanParity` (mirroring the M0044 timestamp / section-11
date tests). This unblocks `timestamptz`-keyed indexes generally, not only this
spec.

### 13. Index-only-scan per-tuple conflict-out against `xmax` (lands `referential-integrity`)

`referential-integrity` is a two-table SSI write skew standing in for an
application-enforced foreign key: `a (i int PRIMARY KEY)`, `b (a_id int)`,
seeded with `a=(1)`. `s1` reads `a WHERE i=1` then `INSERT`s `b VALUES (1)`; `s2`
reads `a WHERE i=1` and the (then-empty) `b WHERE a_id=1` then `DELETE`s
`a WHERE i=1`. Any overlap must raise `40001`. The dangerous structure is the
2-cycle `s1 --rw--> s2` (s1 reads the `a` row `s2` deletes) **and**
`s2 --rw--> s1` (s2's empty read of `b` misses `s1`'s insert).

Before this slice **35 of the 36** generated permutations matched PG 18.3; the
single failure was `rx2 ry2 wx2 rx1 wy1 c1 c2` — the ordering where the pivot
`s1` commits *first* and `s2` (committing last) must abort. Its sibling
`…c2 c1` (commit order reversed) already aborted, which localized the gap: one of
the two rw-edges was missing, and the pre-commit 2-cycle walk (which skips
*already-committed* pivots) only fired when the still-in-flight partner committed
first.

Root cause: the **`s1 --rw--> s2`** edge never installed. `rx1` selects only the
indexed primary-key column (`SELECT i FROM a WHERE i=1`), so the planner picks an
**index-only scan**. `indexOnlyScanOp` (`internal/executor/operators_indexonly.go`)
took the relation-grain SIREAD (phantom protection) and, in its non-`ALL_VISIBLE`
heap-fetch fallback, the invisible-tuple phantom edge — but for a **visible**
tuple it decoded and returned the row *without* the per-tuple conflict-out check
that the heap-fetching index-scan (`operators_index.go`) and seq-scan
(`operators_storage.go`) paths both run. So when `s1`'s index-only scan observed
the `a(i=1)` row that in-flight `s2` had `DELETE`d (visible to `s1` because the
delete is uncommitted; its header carries `Xmax = s2`), no reader→deleter edge
formed. Mirrors upstream: an index-only scan that must fetch the heap (VM bit
cleared by the concurrent delete) runs `HeapCheckForSerializableConflictOut`,
which inspects `xmax`.

Fix (executor-only, **zero** mvcc/SSI-engine change): the `found` (visible) branch
of `indexOnlyScanOp`'s fallback now calls
`ssiRecordTupleRead(ctx, heapRel, ptr.Block, actualSlot, tuple.Header.Xmin,
tuple.Header.Xmax)` on the HOT-resolved live version, exactly like the index-scan
path. The helper short-circuits for RC/RR; its tuple-grain SIREAD is pruned by the
relation-grain lock already held, so only the conflict-out takes effect — against
`xmin` (a concurrent inserter) and, when distinct, `xmax` (a concurrent
deleter/updater). With both edges present the pre-commit 2-cycle walk dooms the
in-flight partner regardless of commit order, so the second committer aborts where
PG does. All 36 permutations now match PG 18.3 byte-for-byte
(`TestPort_IsolationReferentialIntegrity`, stable runs); the 14 previously-passing
SERIALIZABLE specs (incl. `read-only-anomaly-2`, the `read-write-unique` family,
`project-manager`, `total-cash`, `two-ids`) remain green — the added edge is the
precise per-tuple anti-dependency PG records, not a coarse lock, so it does not
over-abort.

### 14. Seconds-less `timestamptz` literal parse (re-lands `classroom-scheduling`)

Section 12 made the `timestamptz` *B-tree key* work, but the spec still failed its
`global setup` INSERT with `invalid timestamp "2010-04-01 10:00" (22007)`. The
follow-up note here originally framed this as a ~50% flake; running the dedicated
test with the Go build cache disabled (`-count=1`) showed it was a **deterministic
100% failure** — the earlier "pass" was a stale cached test result, and `ok` from a
cached run masks a `--- SKIP` (see the test-harness memory note). So section 12's
"sole blocker was the B-tree key type" was incomplete: there was a second,
co-equal blocker in literal parsing.

Root cause: `classroom-scheduling` (and `receipt-report`) book rooms on the
half-hour with seconds-less literals like `TIMESTAMP WITH TIME ZONE
'2010-04-01 10:00'`. `evalTypedStringLit` (`internal/executor/expr.go`, the
`timestamp`/`timestamptz` case) only tried the layouts
`2006-01-02 15:04:05.999999`, `2006-01-02 15:04:05`, and `2006-01-02` — none of
which accept an `HH:MM` time with no seconds, so every overlap literal in the spec
errored. PostgreSQL's `timestamptz_in` accepts a seconds-less time and an optional
numeric timezone offset.

Fix (executor-only, additive to the layout list): the case now also tries
`2006-01-02 15:04` plus the timezone-suffixed variants
(`…15:04:05.999999-07`, `…15:04:05-07`, `…15:04-07`). The tz-bearing layouts are
listed first so an explicit offset is honoured (converted to UTC) before the
zone-less fallbacks treat the wall clock as UTC; a zone-less layout rejects a
tz-bearing input via Go's "extra text" error, so ordering never mis-parses. The
full-second forms TPC-H / pgbench depend on stay in the list unchanged, and the
per-node `CacheValid` memoization means the extra layouts cost only one first-eval
pass. Regression: `TestEvalTypedStringLitTimestampForms`
(`internal/executor/storage_ddl_timestamptz_test.go`) pins seconds-less, explicit
seconds, fractional, date-only, and offset forms for both `timestamp` and
`timestamptz`, plus rejection of out-of-range values.
`TestPort_IsolationClassroomScheduling` now passes 5/5 (was 0/8 SKIP), and all 16
SSI / timestamp specs re-verified green.

### 15. `debug_parallel_query` no-op GUC (lands `serializable-parallel`)

`serializable-parallel` is the O'Neil read-only anomaly of `read-only-anomaly-2`
verbatim — same `bank_account(X,Y)` table, same `s1ry s1wy / s2rx s2ry s2wx`
write-skew core, same read-only `s3` whose `SELECT … WHERE id IN ('X','Y')`
observation of `s1`'s committed write closes the dangerous cycle that dooms
`s2wx` with `40001`. The only difference is that `s3`'s session setup runs
`SET debug_parallel_query = on` so upstream executes `s3r` in a parallel worker.

goopg has no parallel executor, so the GUC has no semantic effect; serial and
parallel SSI outcomes are identical. The lone blocker was registration: section-9
per-tuple SIREAD locks on a single-column full-key index already produce the
correct conflict graph for `s3`'s two PK point reads (the same machinery that
lands `predicate-lock-hot-tuple`'s `IN`-list reads), but `debug_parallel_query`
(renamed from `force_parallel_mode`) was absent from the GUC registry, so the
session-setup `SET` failed with `unrecognized configuration parameter` before any
step ran. This corrects section 8's pessimistic claim that the whole
`serializable-parallel` family needed finer per-access-method locking — the
**base** member needed only the GUC.

Fix (config-only, additive): `debug_parallel_query` is now registered in
`internal/config/defaults.go` as a no-op developer enum (`off`/`on`/`regress`,
boot `off`, `PGC_USERSET`) mirroring `postgres/src/backend/utils/misc/guc_tables.c`.
`SET` succeeds and stores the value; nothing in the planner consults it.
Regression: `TestDebugParallelQueryGUC` (`internal/config/debug_parallel_query_test.go`)
pins the registration, enum membership, case-insensitivity, and rejection of an
out-of-enum value; `TestPort_IsolationSerializableParallel` passes both
permutations byte-for-byte vs PG 18.3. The `-2`/`-3` members land in section 16.

### 16. `ALTER TABLE … SET (reloptions)` (lands `serializable-parallel-2` / `-3`)

The `-2`/`-3` siblings exercise PG's `SXACT_FLAG_RO_SAFE` read-only-safe-snapshot
optimisation *in a parallel worker*: a `SERIALIZABLE READ ONLY` transaction that
PG proves cannot complete a dangerous structure is flagged `RO_SAFE` and released
early, and the variant forces the read to run in a parallel index-only scan.
Crucially, **neither spec writes anything** — `s1`/`s3` are `SERIALIZABLE` but
issue only `SELECT`, and `s2`/`s4` are `SERIALIZABLE READ ONLY`. A workload with
no writes has no rw-antidependency and therefore no dangerous structure, so the
*observable* outcome is anomaly-free: every step commits and each read returns the
same stable snapshot (`COUNT(*) = 100` for `-2`; the full 10-row table for `-3`).
goopg has neither a parallel executor nor an explicit `RO_SAFE` early-release, but
because the outcome is anomaly-free it matches PG 18.3 byte-for-byte regardless —
the same reason the base member does (section 15).

The lone blocker was the setup DDL `ALTER TABLE foo SET (parallel_workers = 2)`,
which goopg's parser rejected (`expected ADD or DROP`) — the table-level
`SET (reloptions)` / `RESET (reloptions)` form was unimplemented. The parallel
cost GUCs the sessions set (`parallel_setup_cost`, `parallel_tuple_cost`,
`min_parallel_index_scan_size`, `parallel_leader_participation`, `enable_seqscan`)
were already registered.

Fix (additive): the parser now recognises `ALTER TABLE name SET (param = value, …)`
and `RESET (param, …)` (`AlterTableSetReloptions` / `AlterTableResetReloptions` in
`internal/parser/ast.go`; dispatch in `parseAlterTableAction`, guarded so the
distinct `SET SCHEMA` / `SET TABLESPACE` / `SET LOGGED` actions still fall through
to the ADD/DROP path). The executor (`execAlterTableSetReloptions` in
`internal/executor/operators_ddl.go`) **merges** the named storage parameters into
the live `catalog.Table` fields that the virtual `pg_class` builder renders into
`pg_class.reloptions`, so the change is immediately visible to `pg_dump` / `pg_class`
with no heap re-sync (unlike the `pg_attribute`-backed per-column options). Values
are bounds-checked with the exact ranges and SQLSTATEs of the `CREATE TABLE WITH`
path, and — matching PG's statement atomicity — the whole option list is validated
into a pending set *before* any field is committed, so a multi-option `SET` with
one out-of-range value leaves the relation untouched (Go map order is unspecified,
so a field-by-field apply would otherwise leave a nondeterministic partial state).
`RESET` clears the named parameters back to their unset defaults; unmodelled but
lowercase option names are accepted and ignored, exactly as `CREATE TABLE WITH`.

Regression: `TestAlterTableSetReloptions{MatchesCreateWith,Merge,Bounds,Atomic}`
(`internal/executor/operators_alter_set_reloptions_test.go`) pin the sibling-path
lock-step with `CREATE TABLE WITH`, the merge/RESET semantics, the bounds SQLSTATEs,
and the all-or-nothing atomicity; `TestPort_IsolationSerializableParallel2` and
`…Parallel3` pass byte-for-byte vs PG 18.3. Isolation pass count 27 → 29.

### 17. `READ ONLY DEFERRABLE` safe-snapshot deferral (lands `read-only-anomaly-3`)

`read-only-anomaly-3` is the example from O'Neil's *"A read-only transaction
anomaly under snapshot isolation"*. Its read-only session `s3` is declared
`BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE READ ONLY DEFERRABLE`. Upstream
avoids the anomaly **without aborting anyone** by deferring `s3`'s snapshot
(`GetSafeSnapshot`, `predicate.c`) until a *safe* snapshot is available: `s3r`
blocks (`<waiting ...>`) while the concurrent read-write `s2` is still active,
then completes once `s2` commits, observing the final committed state
(`X=-11`, `Y=20`). The single permutation must produce zero serialization
failures.

Two blockers were fixed:

1. **`DEFERRABLE` was never parsed.** `DEFERRABLE` lexes as the unreserved
   keyword token `KwDeferrable`, but `parseBeginModes` matched it with
   `acceptIdentKeyword("deferrable")`, which only accepts a `TokenIdent`. The
   word was therefore never consumed and `BEGIN … READ ONLY DEFERRABLE` failed
   with `syntax error … got deferrable`. The fix matches it with
   `acceptKeyword(KwDeferrable)` (and the `NOT DEFERRABLE` arm likewise), and
   records the result in the new `BeginStmt.Deferrable` / `Transaction.Deferrable`
   plan field. (The earlier "accepted, no-op for v0" comment was wrong — the
   prior code silently rejected the clause; no spec had exercised it.)

2. **The safe-snapshot deferral did not exist.** goopg now models the minimal
   `GetSafeSnapshot`:
   - `SerializableXact` gains `ReadOnly` / `Deferrable` flags (the
     `SXACT_FLAG_READ_ONLY` / `SXACT_FLAG_DEFERRABLE` analogues), set right after
     `Begin` by `Manager.MarkSerializableModes`, wired from both BEGIN paths (the
     `transactionOp.execBegin` operator and the inline auto-commit→explicit
     promotion in `server/dispatch.go` — sibling paths kept in sync).
   - `Manager.SnapshotFor`, when a SERIALIZABLE xact takes its **first** snapshot
     (`firstSnap == nil`), calls `Manager.waitForSafeSnapshot`. For a
     `ReadOnly && Deferrable` xact this enrolls every concurrent *declared
     read-write* SERIALIZABLE xact active at that instant and blocks on a
     `sync.Cond` (`ssiCond`, bound to `ssiMu`) until each has `FinishedAt`
     stamped (committed **or** aborted). `releaseSerializableLocked` broadcasts
     `ssiCond` on every finish. A non-deferrable or write-side xact returns
     immediately, so the only behavioural change for all other SERIALIZABLE
     workloads is one extra uncontended `ssiMu` acquisition per transaction.

   A snapshot taken after every concurrent writer has drained cannot place the
   read-only xact on the rw-conflict *out* side of a dangerous structure, so it
   is always **`RO_SAFE`** and never aborted. goopg deliberately does **not**
   implement the `RO_UNSAFE` early-abort/retry refinement (where a committing
   writer can mark the deferrable reader unsafe, forcing a fresh snapshot): no
   ported spec exercises it — the sole `DEFERRABLE` spec resolves to a safe
   snapshot. That divergence is acceptable because the deferral is purely a
   *false-positive eliminator*: in the worst un-modelled case goopg would wait
   slightly longer, never produce a wrong answer.

The runner's existing 300 ms block-detection (`isolation_runner.go`) turns the
server-side `ssiCond.Wait` into the `<waiting ...>` / `<... completed>` markers
with no runner change. Verified by `TestPort_IsolationReadOnlyAnomaly3`
(byte-for-byte vs PG 18.3) plus the deterministic `TestSafeSnapshot*` mvcc unit
suite (waits for a committing writer, an aborting writer, returns immediately
with no writers / when not deferrable / when only read-only peers are active).
Isolation pass count 29 → 30.

### 18. Relation-wide conflict-in for `REFRESH MATERIALIZED VIEW` (lands `matview-write-skew`)

`matview-write-skew` runs two SERIALIZABLE sessions: `s1` does
`REFRESH MATERIALIZED VIEW CONCURRENTLY order_summary` (which reads the parent
`orders` and rewrites the matview), and `s2` reads the matview
(`SELECT max(date) FROM order_summary`) then writes the parent (`INSERT`/`UPDATE`
on `orders`). Every overlap is a dangerous structure, so the second committer —
always `s2`, since `s1_commit` precedes `s2_commit` in all eight permutations —
must abort with `40001`.

goopg already passed the **four** `refresh`-before-`read` permutations: there,
`s1` rewrites the matview first, so `s2`'s later read sees the old row carrying
`s1`'s concurrent `xmax` and forms the `s2 → rw s1` edge via the read-path
conflict-out (`ssiRecordTupleRead`, section 13). It **failed** the four
`read`-before-`refresh` permutations, where `s2` reads the matview before `s1`
touches it. There the `s2 → rw s1` edge can only come from the *write* side:
`s1`'s rewrite must conflict-in against `s2`'s existing SIREAD lock on the
matview. But `execRefreshMatView` truncates and re-populates the matview heap
through the low-level `writeHeapRow` path, which — unlike the INSERT operator —
never calls `ssiRecordTupleWrite`. The refresh therefore produced **no** write
tag at all, and even if it had, `ssiRecordTupleWrite`'s conflict-in walk is
**upward-only** (`tuple → page → relation`) and could never reach `s2`'s
fine-grained per-tuple SIREAD on the matview.

The fix mirrors upstream's `CheckTableForSerializableConflictIn` (`predicate.c`),
which TRUNCATE and DROP use for exactly this "logical mass delete of the whole
relation" shape:

- `Manager.CheckTableForSerializableConflictIn(writerHandle, db, rel)` (and its
  `…ReportingFailure` variant) scans the global predicate-lock target registry
  for **every** tag matching `(db, rel)` at **any** granularity — relation, page,
  or tuple — and installs an `R → W` rw-edge from each holder, plus the retained
  committed-reader walk (the same two-loop shape as
  `checkForSerializableConflictInLocked`). It is the only conflict-in path that
  finds a holder of a *finer* lock than the writer's target, which is precisely
  what a whole-relation rewrite needs.
- The executor hook `ssiRecordTableWrite` wraps it and is invoked from
  `execRefreshMatView` just before the truncate (so a doomed-writer abort leaves
  nothing half-written). It returns `40001` only if the refresh newly dooms
  *itself* as a pivot to an already-committed partner; the spec's deferred-pivot
  shape returns nil and surfaces at `s2`'s `COMMIT`.

No read-side change was needed: the matview scan already acquires per-tuple
SIREAD locks (`ssiRecordTupleRead` is not gated on `relkind`), and upstream's
`PredicateLockingNeededForRelation` excludes only system catalogs and temp
relations — **not** matviews. The misleading "matviews never participate in
predicate locking" comment on `ssiRecordRelationRead` was corrected.

Two runner-fidelity pieces (shared with the still-deferred `receipt-report`)
also landed here: `date` columns now render `MM-DD-YYYY` (PG regress runs with
`DateStyle='Postgres, MDY'`, so `2022-04-01` prints `04-01-2022`) by scanning
such columns as `sql.NullTime` rather than letting lib/pq's `time.Time` decode
re-render `RFC3339`; and `pqprintFormat` now replicates libpq `PQprint`'s
**content-based** right-justification heuristic (`fe-print.c`) — a column
right-justifies unless some value holds a character outside `[0-9.Ee -]` or does
not end in a digit — which is why the all-digits-and-dashes `04-01-2022` aligns
like an integer. Verified by `TestPort_IsolationMatviewWriteSkew` (byte-for-byte
vs PG 18.3, all eight permutations) plus the `TestCheckTableForSerializableConflictIn`
mvcc unit suite (tuple/page/relation holders, different-relation and self no-ops,
multi-reader fan-out, deferred-pivot-returns-nil, retained committed reader).
Isolation pass count 30 → 31.

### 19. De-facto `READ ONLY` SSI modeling (lands `receipt-report`)

`receipt-report` is the daily-receipts read-only anomaly: `s1` (`rxwy1`) reads the
control row's deposit date and `INSERT`s a receipt stamped with it, `s2` (`wx2`)
advances the control date, and `s3` (`rx3`/`ry3`) — declared
`SERIALIZABLE READ ONLY` — reads the control row then the day's receipts. Of the
**210** generated permutations only **6** are genuine serialization failures: the
ones where `s1` overlaps both `s2` and `s3` *and* `s2` commits before `s3`'s first
`SELECT`. The spec's own header notes that as long as `s3` is `READ ONLY` there
must be **no** false positives.

goopg over-aborted **48 vs 6** because — as documented since section 8 — it modeled
every SERIALIZABLE xact as read-write and so let the read-only `s3` close
dangerous structures it can only *appear* to precede. The fix ports upstream's
de-facto `READ ONLY` machinery (`predicate.c`):

- **Snapshot watermark.** `SerializableXact.SnapshotSeqNo` is captured the first
  time the xact takes a statement snapshot (deferred to the first non-`BEGIN`
  statement in `Manager.SnapshotFor` — *not* at `Begin`, where `BeginAt` is
  stamped). It is the analogue of `SeqNo.lastCommitBeforeSnapshot`: because
  `FinishedAt` and `SnapshotSeqNo` draw from the one `nextCommitSeqNo` counter, a
  peer committed strictly before this xact's snapshot iff
  `peer.FinishedAt < SnapshotSeqNo` — the new `committedBeforeSnapshot` predicate.
  `BeginAt` cannot be reused here: all three sessions `BEGIN` at permutation start,
  but `s3`'s anomaly hinges on whether `s2` committed before `s3`'s *first SELECT*.
- **Conflict-out skip (the load-bearing change).** In
  `checkForSerializableConflictOutLocked`, a `READ ONLY` reader that reads an
  already-committed writer records **no** rw-conflict unless the writer holds an
  out-conflict to a transaction that committed before the reader's snapshot
  (`∃ t2 ∈ writer.outConflicts: committedBeforeSnapshot(t2, reader)`). This mirrors
  `CheckForSerializableConflictOut` lines 4123-4137 and must run *before* the edge
  is recorded, because `Case 1` of `onConflictCheckLocked` (committed writer with
  `ConflictOut`) would otherwise fire the instant a `READ ONLY` reader touched any
  committed writer holding an out-conflict — regardless of snapshot order. The
  live-`outConflicts` scan stands in for upstream's
  `SXACT_FLAG_CONFLICT_OUT` + `earliestOutConflictCommit` pair.
- **`OnConflict` / `PreCommit` refinements.** `onConflictCheckLocked` Case 2 now
  carries the `(!reader.ReadOnly || committedBeforeSnapshot(t2, reader))` clause,
  Case 3 is gated on `!reader.ReadOnly` (a read-only xact writes nothing so can
  never be a pivot) with the symmetric `t0` clause, and the `PreCommit`
  far-conflict scan skips a `READ ONLY` in-flight `Tin` — exactly upstream's
  `!SxactIsReadOnly(...)` guards.

The skip can only ever *suppress* an edge, never add one, so it can fix false
positives without risking a false negative for the read-write specs; the changes
are all gated on the declared `ReadOnly` flag, so only `serializable-parallel-2`/`-3`
and `read-only-anomaly-3` (the other declared-`READ ONLY` specs) share the paths,
and all three were re-verified byte-for-byte. Verified by
`TestPort_IsolationReceiptReport` (all 210 permutations vs PG 18.3) plus the new
`TestCommittedBeforeSnapshot` and `TestCheckForSerializableConflictOut_ReadOnly*`
mvcc unit tests. The runner date-render / `PQprint` alignment pieces this spec
also needs landed earlier in section 18. Isolation pass count 31 → 32.

### 20. Index-only-scan write skew + multi-block global setup (lands `index-only-scan`)

`index-only-scan` is a SERIALIZABLE write skew across two all-visible tables.
Both `tabx` and `taby` are populated with 10000 rows, given a `PRIMARY KEY`, and
`VACUUM FREEZE ANALYZE`d so the whole heap is all-visible (the precondition for an
index-only scan). `s1` (`rxwy1`) runs `DELETE FROM taby WHERE id = (SELECT min(id)
FROM tabx)` and `s2` (`rywx2`) runs the mirror `DELETE FROM tabx WHERE id =
(SELECT min(id) FROM taby)`. Each `SELECT min(id)` reads one table and the `DELETE`
writes the other, so every overlap forms the `s1 →rw s2 →rw s1` cycle and the
second committer must abort with `40001`; the two fully serialized orderings
(`rxwy1 c1 rywx2 c2` and `rywx2 c2 rxwy1 c1`) commit cleanly. All six generated
permutations match PG 18.3 byte-for-byte.

This is a **zero-SSI-change** promotion: the existing single-column full-key
index-scan / index-only-scan SIREAD granularity (sections 9 and the
`referential-integrity` index-only fetch hook) already records the per-tuple reads
that cross-cover the rows the partner `DELETE`s, so the dangerous structure closes
with no engine change. Three harness blockers had to clear:

- **Floating-point GUC values.** `parser.parseSetValueAtoms` accepted integer,
  string, and identifier atoms but not a `TokenNumericLit`, so the session setup
  `SET LOCAL seq_page_cost = 0.1` failed with a `42601` syntax error
  (`expected value`). It now accepts `TokenNumericLit` (and a leading minus on
  either an int or a numeric) — real-typed GUCs are set with fractional literals
  upstream. Purely additive: it rejects nothing previously accepted.
- **Per-setup-block submission (the load-bearing harness fix).** The isolation
  spec parser stored the global `setup {}` body in a single `SetupSQL` string and
  **overwrote** it on each block, so a spec with multiple global setup blocks kept
  only the last. `index-only-scan` is the first such spec: its three blocks are the
  table-creation block plus `VACUUM FREEZE ANALYZE tabx` and `… taby`, so only the
  final VACUUM survived and the `DELETE` steps all failed with `relation "tabx"
  does not exist`. Fixed by collecting blocks into `IsolationSpec.SetupBlocks` and
  having `RunSpec` submit each block on its own `execConn` call — mirroring
  isolationtester.c running each `setupsqls[]` entry via its own `PQexec`. This
  also makes `VACUUM` correct: a standalone block is its own implicit transaction,
  so `VACUUM` no longer aborts with "cannot run inside a transaction block" the way
  it would if concatenated with the DDL/DML into one multi-statement submission.
  `SetupSQL` is still populated (all blocks joined) for debug and as a fallback.
- **`cpu_*_cost` real GUCs.** `cpu_tuple_cost`, `cpu_index_tuple_cost`, and
  `cpu_operator_cost` were unregistered, so the session setup `SET LOCAL
  cpu_tuple_cost = 0.03` would have raised `unrecognized configuration parameter`.
  They are now registered as `TypeReal` (`ContextUserset`, boot values matching
  guc_tables.c) and added to `postgresql.conf.sample`. goopg's planner does not
  consume them yet — like the parallel cost GUCs they are inert no-ops whose only
  job is to be SET-able for upstream specs that tune the planner.

Verified by `TestPort_IsolationIndexOnlyScan` (all six permutations vs PG 18.3) and
by re-running `simple-write-skew`, `temporal-range-integrity`, and
`referential-integrity` (the structural twins / single-setup-block specs) green to
confirm the per-block submission change is regression-free. Isolation pass count
32 → 33.

## Slice M0118-0003 — `nowait` (FOR UPDATE NOWAIT, tuple-level fail-fast)

`nowait.spec` locks the single row of `foo` with `SELECT * FROM foo FOR UPDATE
NOWAIT` in two overlapping sessions. When `s1` already holds the row lock, `s2`'s
`FOR UPDATE NOWAIT` must immediately raise `ERROR: could not obtain lock on row in
relation "foo"` (`ERRCODE_LOCK_NOT_AVAILABLE` = SQLSTATE `55P03`, the message from
`heapam.c`) rather than block. Two parser + executor blockers had to clear:

- **Trailing `LIMIT` after the locking clause (parser).** PostgreSQL's `gram.y`
  `select_no_parens` permits `select_limit` either *before* or *after* the
  `for_locking_clause`: both `… LIMIT n FOR UPDATE` and `… FOR UPDATE [SKIP LOCKED
  | NOWAIT] LIMIT n` are legal. goopg only handled the former, so the
  `skip-locked` / `nowait` spec shape `… FOR UPDATE SKIP LOCKED LIMIT 1` failed with
  a syntax error. The limit/offset/fetch parsing was factored into
  `parser.parseSelectLimitClauses` and is now invoked at both sites: once before the
  set-op/locking tail, and again after the locking loop when a locking clause was
  parsed and no limit preceded it. (`nowait.spec` itself has no `LIMIT`, but the
  fix is shared with the `skip-locked` family and is the precondition for them.)

- **Cross-statement row-lock conflict detection (executor).** goopg's lock manager
  is **statement-scoped**: `dispatch.go` assigns a fresh `BackendID` per Query
  message and calls `LockMgr.ReleaseAll` at each statement's end, so a tuple lock
  taken by `s1a` is already released by the time `s2a` runs. Cross-statement
  locking therefore rides entirely on the **persisted lock-only xmax** stamped into
  the heap page (`HeapXmaxLockOnly` + the strength bits), the same durable signal
  `stampLockInner` already reads for real updaters. Previously `stampLockInner`
  *ignored* lock-only xmax (`!IsHeapTupleLockOnly` guard) and silently overwrote it
  — so a second `FOR UPDATE` clobbered the first session's lock instead of
  conflicting. `stampLockInner` now detects a conflicting lock-only xmax held by a
  still-active transaction (`tupleLockConflicts` + `TxnMgr.IsXIDActive`) and, **for
  NOWAIT only**, returns the relation-qualified `55P03`. The default blocking path
  and SKIP LOCKED are deliberately left untouched (see scope note), so no
  currently-passing spec changes. `lockRowsOp` resolves `waitPolicy` and
  `lockRelName` once at Open from `Locks[0]`; `stampLock`'s top-level acquisition
  also switches to `tryAcquireTupleLock` under NOWAIT to fail fast on the rare
  intra-statement race.

`tupleLockConflicts` encodes the single-locker (non-multixact) row-lock conflict
matrix over goopg's two effective strengths: FOR UPDATE (`HeapXmaxExclLock`)
conflicts with any held lock; a shared request conflicts only with a pure-exclusive
holder.

**Scope boundary (deferred siblings).** Only `nowait` lands this slice. `nowait-2`
needs multixact (two `FOR SHARE` holders then an upgrade); `nowait-3` needs the
blocking-`FOR UPDATE`-on-a-locked-row path (intentionally not implemented here);
`nowait-4` / `nowait-5` need `EvalPlanQualFetch` NOWAIT over an updated CTID chain
plus advisory locks / PREPARE; `lock-nowait` needs **cross-statement relation-lock
persistence** (`LOCK TABLE … NOWAIT`), which goopg's statement-scoped lock manager
does not provide. The whole `skip-locked` family additionally needs the
`Limit`-above-`LockRows` plan shape — goopg currently plans
`LockRows(Project(Limit(Sort(Scan))))`, applying `LIMIT` *below* the lock, so a
skipped row reduces the result count instead of pulling the next lockable row as PG
does (`Limit → LockRows → Sort`). These are tracked in the deferral ledger.

Verified by `TestPort_IsolationNowait` (all six permutations vs PG 18.3), the new
`TestParseSelectForUpdateBeforeLimit` and `TestTupleLockConflicts` unit tests, and
by re-running `lock-committed-update` (the other tuple-lock spec) green to confirm
the blocking path is regression-free. Isolation pass count 33 → 34.

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
  source of section 10), `temporal-range-integrity` (cross-table temporal
  write skew; zero SSI change — only the `date` B-tree key type of section 11),
  and `classroom-scheduling` (double-booking write skew; zero SSI change — only
  the `timestamptz` B-tree key type of section 12), and `serializable-parallel`
  (the `read-only-anomaly-2` cycle with a parallel-worker read-only `s3`; zero SSI
  change — only the `debug_parallel_query` no-op GUC of section 15), and
  `serializable-parallel-2` / `-3` (write-free `RO_SAFE` read-only workloads whose
  outcome is anomaly-free; zero SSI change — only the `ALTER TABLE … SET (reloptions)`
  DDL of section 16), and `read-only-anomaly-3` (the O'Neil read-only anomaly with a
  `SERIALIZABLE READ ONLY DEFERRABLE` `s3` deferred to a safe snapshot — the
  `GetSafeSnapshot` drain-the-writers wait of section 17; `s3r` blocks then commits
  with no abort), and `matview-write-skew` (write skew between a
  `REFRESH MATERIALIZED VIEW CONCURRENTLY` and a reader/writer of the matview's
  parent — the relation-wide `CheckTableForSerializableConflictIn` refresh hook of
  section 18; all eight permutations abort the second committer with `40001`), and
  `receipt-report` (the daily-receipts read-only anomaly with a declared
  `SERIALIZABLE READ ONLY` `s3` — the de-facto `READ ONLY` SSI modeling of
  section 19; all 210 permutations match PG with exactly 6 genuine serialization
  failures, down from goopg's previous 48), and `multiple-row-versions` (the
  four-xact dangerous structure over many row versions — SSI-correct via the
  section-9 index-scan SIREAD granularity; its 1,000,000-row single-transaction
  setup INSERT no longer trips the WAL-buffer ring race after the section-0107-0007aj
  `2*conservativeSize` segment-crossing pad reservation, verified 3/3 uncached
  runs at ~65s), and `index-only-scan` (the two-table `SELECT min(id)` write skew
  through index-only scans — zero SSI change; only the floating-point GUC parse,
  the per-setup-block submission runner fix, and the `cpu_*_cost` GUCs of section 20).
- **Still deferred (same slice family):** none — every spec in the M0118-0001
  group now passes. The dedicated `TestPort_Isolation*` functions auto-promote
  (run, then `t.Skip` only on `defer`), so the next slice sees green→pass instantly.
