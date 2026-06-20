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

## Status / scope boundary

- **Passing:** `simple-write-skew` — the 2-transaction, 2-cycle write-skew
  dangerous structure. goopg's SSI detects it and aborts one committer with
  `40001` in every overlapping interleaving.
- **Still deferred (same slice family):** `two-ids`, `total-cash`,
  `receipt-report`, `project-manager`, `classroom-scheduling`. These exercise
  **3+ transaction** read-only / multi-version anomalies; goopg's SSI currently
  yields a **false negative** (it fails to raise `40001` in some permutations
  where PG does), e.g. `two-ids` is missing the expected serialization error in
  the `wx1 rxwy2 ry3 …` ordering. Closing these is SSI dangerous-structure
  *completeness* work (pivot detection across read-only and multi-version
  pivots), tracked under M0118-0001 with a deferral-ledger entry — not an output
  or error-format issue. Their dedicated `TestPort_Isolation*` functions are kept
  as `t.Skip` promotion anchors so the next slice sees green→pass instantly.
