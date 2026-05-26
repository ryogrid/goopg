# Detoast Pointer Sanity Guards (M0097)

- Status: accepted
- Date: 2026-05-24
- Supersedes: none

## Context

`TestPort_RegressSuite` was unstable during the M0097 pg_regress sweep. A
corrupted or accidental TOAST pointer could survive row decode and reach
`internal/executor/toast.go::DetoastValue`, which trusted the 12-byte pointer's
`total_len` and `num_chunks` fields and allocated `make([][]byte, numChunks)`
before validating them against any physical limit.

That turned malformed pointers into process-wide failures:

1. `num_chunks` could explode into the millions or billions, triggering
   unbounded allocations and eventually `fatal error: runtime: out of memory`.
2. Even when the server survived, the regress harness lost its live cluster and
   later cases devolved into connection-refused noise rather than actionable
   SQL diffs.

## Decision

Add hard sanity guards inside `DetoastValue` before any allocation:

1. Reject `num_chunks <= 0` unless `total_len == 0`.
2. Reject `num_chunks > 1<<20` as implausible for goopg's TOAST format.
3. Reject `total_len > num_chunks * ToastMaxChunkSize`.
4. Reject `total_len > maxDetoastChunks * ToastMaxChunkSize`.

These checks intentionally mirror the defensive posture already added to
`decodeGoopgRowIntoMctx` for accidental PG-physical-row fallthrough, but they
live at the final reassembly point so every detoast caller is protected even if
an invalid pointer arrives through another path.

## Consequences

- Corrupted TOAST pointers now fail closed with a normal executor error instead
  of killing the backend or the whole server.
- `seqScanOp`/`indexScanOp` can continue using the existing "skip
  undetoastable tuple" path rather than propagating catastrophic runtime
  failures.
- M0097 regress runs become stable enough to report real parity gaps again.

## Verification

- `go test ./internal/executor -run 'TestDetoastValueRejectsImplausibleChunkCount|TestDetoastValueRejectsImplausibleTotalLength|TestToastRoundTripDoD|TestToastMultipleChunks'`
- `go test -v -run TestPort_RegressSuite -timeout 30m ./internal/testport/`
