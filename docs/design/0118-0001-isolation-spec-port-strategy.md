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

## Status / scope boundary

- **Passing:** `simple-write-skew` (2-cycle write skew), `two-ids` (3-xact
  read-only anomaly, all 90 generated permutations byte-identical to PG 18.3),
  `total-cash` (mid-statement read-path abort, all 20 permutations — section 4),
  `project-manager` (phantom predicate locking, all 21 permutations
  byte-identical to PG 18.3 — section 5), and the full `read-write-unique` family
  `{base, -2, -3, -4}` (unique-constraint write skew, sections 6–7; `-4` mixes
  `40001` and `23505` across its three permutations via the conflict-in walk + the
  index-scan SSI completeness fixes).
- **Still deferred (same slice family):**
  - `classroom-scheduling` — the SSI machinery is in place (section 5), but its
    primary key is `(room_id text, start_time timestamptz)` and goopg's btree
    rejects a `timestamptz` key (`btree v0 only supports int4 / numeric keys`,
    SQLSTATE 0A000) at the spec's `global setup`. Blocked on btree key-type
    support, NOT on SSI — a different subsystem.
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
  Their dedicated `TestPort_Isolation*` functions auto-promote (run, then
  `t.Skip` only on `defer`), so the next slice sees green→pass instantly.
