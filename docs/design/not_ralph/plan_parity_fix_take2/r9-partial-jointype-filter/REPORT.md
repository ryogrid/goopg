# R9 results — the filter, and a measurement artefact worth killing

*Round 9 of `../TODO.md`. Design: `DESIGN.md` (committed `aae61fe38`).
Implemented, gated and measured 2026-09-08.*

## 1. Verdict

Landed; every prediction held; K17's blocker on R10 is cleared.

| check | result |
|---|---|
| **R8's crash reproduction** — TPC-DS Q5 under `GOOPG_GATHER_PATHS=all` | **plans (50-row plan), no panic** |
| TPC-H default-off plans | **byte-identical to R7** |
| TPC-DS default-off plans | **byte-identical to R7** |
| TPC-H values | 22/22 byte-identical |
| TPC-DS SF0.5 values | `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` |
| optimizer + executor suites | green |

## 2. What landed

`partialHashJoinTypeOK(jt)` in `parallel.go`, next to
`hashJoinIsPartialCapable`, admitting `{INNER, LEFT, SEMI, ANTI}` — the
set where PG's `hash_inner_and_outer` parallel block and goopg's own
executor predicate coincide — and wired into
`addPartialHashJoinPath`'s guard block.

The stale comment in `assertParallelAwareJoinIsRunnable` is **corrected
in place**, not quietly replaced: it now records that the filter it used
to describe did not exist, that the panic below was therefore reachable
rather than unreachable, and that it fired on TPC-DS Q5. That history is
the useful part — the comment is why the defect survived reading.

### 2.1 The fix is a test, not an `if`

K17 was a *class* bug: a hand-written claim drifting from the code. A
second hand-written list would reproduce it exactly. So
`TestPartialHashJoinTypeOK` pins the producer's verdict **against
`hashJoinIsPartialCapable`** for all seven jointypes. Narrow the
executor predicate and that test fails, instead of the producer silently
over-filing a path that panics at plan build — or, without the
assertion, silently drops rows.

The one-line `if` fixes the instance; the cross-predicate test is what
addresses the thing that actually went wrong.

## 3. A false positive I nearly reported as a plan move

The first default-off comparison said **"TPC-DS plans MOVED"**. They had
not. The entire 12-line diff was `mktemp` filenames:

```
< psql:/tmp/tmp.0aLTanPRn7:29: ERROR:  syntax error at or near ";"
> psql:/tmp/tmp.YZBUuU4nxN:29: ERROR:  syntax error at or near ";"
```

The three unplannable queries (Q36/70/86) echo psql's error text, which
contains the capture script's temp path, so two byte-identical captures
diff every time. Under a plan-pin gate this is a permanent false
positive, and in this round it briefly looked like a change contradicting
the design's central claim.

Fixed at the source rather than filtered at the reader: both capture
scripts now use a fixed `parity-capture-$$.sql` name, with the reason in
a comment, and the copies under `r2-instrument/` are updated. A future
round diffing two captures gets a clean answer.

## 4. Predictions, scored

DESIGN §5 said: default-off byte-identical on both corpora (**correct**,
once §3's artefact was removed); Q5 plans instead of crashing
(**correct**); `Parallel Hash Join` counts drop from R8's 19/132 as
RIGHT joins stop being filed (**not measured this round** — it belongs
to R10, where the flip is actually evaluated, and asserting it here
without measuring would be the error this workstream keeps finding);
no parity movement at the default (**correct**).

## 5. Filed

- **R10 — flip `GOOPG_GATHER_PATHS`**, now unblocked. Full values gates
  both corpora, parity adjudicated per moved plan, and R8's
  `aggregation-strategy` 10 → 14 move explained before acceptance.
- The `Parallel Hash Join` count under `=all` should be re-measured
  there, since R8's 19/132 was taken with RIGHT joins still being filed.
