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
`prepareSeqNo`/`commitSeqNo`). Where upstream would `ereport` **mid-statement**
(Case 1 / Case 3 reader-kill), goopg instead dooms the same transaction, which
fails at its **own COMMIT** — correct abort, later surfacing point. This is why
`total-cash` (whose error lands on the read step `rxy2`) stays deferred this
slice.

## Status / scope boundary

- **Passing:** `simple-write-skew` (2-cycle write skew) and `two-ids` (3-xact
  read-only anomaly, all 90 generated permutations byte-identical to PG 18.3).
- **Still deferred (same slice family):**
  - `total-cash` — needs the **mid-statement** abort: PG cancels the reader
    during `rxy2` (Case 3 reader-kill); goopg defers to COMMIT, so the read step
    still prints its result. Requires threading a `40001` error up from the
    read-path hook through the scan operators.
  - `project-manager`, `classroom-scheduling` — 2-cycles whose second edge needs
    **phantom / empty-range predicate locking** (a `SELECT … WHERE …` over a
    range an INSERT later fills): goopg currently misses the rw-edge, a false
    negative independent of the retention machinery.
  - `receipt-report` — `BEGIN ISOLATION LEVEL SERIALIZABLE, READ ONLY` is a
    parser gap (syntax error today) *and* needs de-facto READ ONLY SSI modeling
    to avoid the 42 false positives upstream notes for a READ WRITE `s3`.
  Their dedicated `TestPort_Isolation*` functions auto-promote (run, then
  `t.Skip` only on `defer`), so the next slice sees green→pass instantly.
