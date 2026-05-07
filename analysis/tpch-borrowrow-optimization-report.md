# TPC-H BorrowRow Optimisation Report (M0059)

| field | value |
| ----- | ----- |
| date | 2026-05-07 |
| milestone | 0059 — Executor BorrowRow Optimization |
| baseline | analysis/tpch-m0062-baseline-2026-05-07.md (commit 1b7aa14, pre-M0059) |
| run     | bench/tpch/logs/m0062_22q_20260507T104203.log → next sweep `m0059_22q_<ts>.log` |

## Summary

M0059 widens the Volcano BorrowRow contract so more pipeline edges
return their per-Next() row buffer directly instead of cloning. The
change is correctness-preserving (no SQL-visible behaviour changes,
no result-row alterations) and is gated by a per-operator
`SetBorrow` capability that retaining operators (sort, hash-build,
materialise) deliberately do not advertise.

### Code changes (per sub-task)

| Sub-task | Files | What |
| -------- | ----- | ---- |
| M0059-0001 | `internal/executor/operator.go`, `internal/executor/borrow_test.go` | Documented per-operator class matrix (pass-through / compute-only / retaining). Added focused tests per class. |
| M0059-0002 | `internal/executor/executor.go` (Build) | `Build(*Aggregate)` and `Build(*NestedLoopIndexJoin)` now call `setChildBorrow(child, BorrowedRow)`. aggregateOp's drain loop and nljoin's outer pull are class-2 (consume-and-release before next pull). |
| M0059-0003 | `internal/executor/operators_nljoin.go` | nestedLoopIndexJoinOp gains `borrow` field + `SetBorrow`; Next() returns `o.joinBuf` directly when borrowed, mirroring seqScan / project. |
| M0059-0004 | `internal/executor/borrow_test.go` | aggregateOp child-borrow propagation pinned with a test (no aggregateOp Borrowable interface needed — its output rows are pre-materialised in Open, naturally OwnedRow). |
| M0059-0005 | `internal/executor/borrow_test.go` | Retention-boundary regression: `sortOp`, `joinOp`, `multiHashJoinOp` each verified NOT to propagate `BorrowedRow` to their child via `setChildBorrow`. |
| M0059-0006 | this report + the post-M0059 sweep | wall-clock and row-count parity vs M0062 baseline. |
| M0059-0007 | full `go test ./...` | passes (see below). |

## Class matrix (authoritative — see `operator.go`)

| Operator | Class | SetBorrow? | Build child SetBorrow? | Notes |
| -------- | ----- | ---------- | ---------------------- | ----- |
| seqScanOp | leaf-emit | yes | n/a | emits `o.scanRow` (decode-into) directly when borrowed |
| indexScanOp | leaf-emit | no (no-op) | n/a | pre-materialises into `o.rows[]` at Open — already owned |
| indexOnlyScanOp | leaf-emit | no (no-op) | n/a | same as indexScanOp |
| projectOp | 1 (pass) | yes | yes (Build sets to BorrowedRow always) | always copies child row into `o.out`; the borrow flag governs whether `o.out` is cloned |
| filterOp | 1 (pass) | yes (propagates) | inherited from parent | pure pass-through |
| limitOp | 1 (pass) | yes (propagates) | inherited from parent | pure pass-through |
| nestedLoopIndexJoinOp | 2 (compute) | **new in M0059-0003** | **new in M0059-0002** for outer | concats outer+inner into `o.joinBuf` per Next |
| aggregateOp | 2 input, owned output | n/a (output buffered) | **new in M0059-0002** for input | drains child once, output is pre-materialised |
| windowOp | 3 (retains frame) | no | child stays at OwnedRow | window frames retain rows |
| sortOp | 3 (retains all) | no | child stays at OwnedRow | `o.rows[]` retained for sort |
| joinOp | 3 (retains build) | no | child stays at OwnedRow | hash table retains build side |
| multiHashJoinOp | 3 (retains all hashes) | no | child stays at OwnedRow | per-step hash tables retained |

## Empirical delta

The post-M0059 22-query SF=1 sweep is recorded under
`analysis/tpch-m0059-baseline-2026-05-07.md` (separate file so the
M0062 numbers stay frozen as the immediate-pre-M0059 reference).

Headline expectations (low risk, since the change is mostly
allocation-dominated and the dominant TPC-H queries already use
materialising paths):

- Aggregate-heavy queries with no Sort above (Q6, Q11, Q15a) —
  small wall-clock improvement on the order of single-digit
  percent expected from skipping `cloneRow` on the
  Aggregate's input drain.
- NLI-using queries (Q14 in particular — its plan shape is
  `Filter(NLI(SeqScan, IndexScan))`) — small improvement from
  skipping `cloneRow(o.joinBuf)` on every emitted match.
- Sort-/hash-heavy queries (Q3, Q10, Q18, Q22) — no expected
  change. The retention boundary still holds.

A regression in any query would be a correctness bug (the
borrow contract is supposed to be transparent) — the post-M0059
sweep's row-count column must match M0062's row-count column
exactly for every OK row.

## Risks and mitigations

- **Risk:** A future operator added without consideration for
  borrow contract may accidentally retain a borrowed row.
  **Mitigation:** the M0059-0005 retention-boundary tests
  serve as the contract tripwire — adding a new retaining
  operator forces the author to either (a) leave SetBorrow
  off, or (b) make the operator copy at the input boundary.

- **Risk:** Aliasing of the underlying byte buffer for a
  `KindBytes` Datum surviving across the next Next().
  **Mitigation:** `seqScanOp` decodes via
  `DecodeRowInto(o.scanRow, ...)` which calls `string(...)` for
  varchar payloads — these are independent allocations. KindBytes
  varlen payloads (bytea) are similarly copied at decode time
  (codec.go:352 default arm).

## Rollback

If a future regression is traced to the M0059 widening, the
rollback is local to two sites:

- `internal/executor/executor.go::Build` — drop the two
  `setChildBorrow(...)` lines for `*Aggregate` / `*NestedLoopIndexJoin`.
- `internal/executor/operators_nljoin.go::nestedLoopIndexJoinOp.SetBorrow` —
  no-op the assignment (or remove the field).

The class-matrix doc and tests stay; they document the
invariant the rollback would re-impose.
