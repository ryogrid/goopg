# 08-06 — FSM.GetCandidates scan cost under buffer pressure

status: design · date: 2026-07-14 · base: `a640d2b0` · gates: G-race, G-tpch,
G-perf → [README](README.md)

## 1. Problem and numbers

New at scale 500: `storage.(*FSM).GetCandidates` is **4.65 % flat / 4.86 % cum**
of `-N` CPU (07-02), the notable engine entry after the syscall/futex floor. It
is the free-space-map candidate scan that INSERT (pgbench_history) and non-HOT
UPDATE spill use to find a page with room. It did not appear at scale 100 — it
grows with relation size, so it must be confirmed non-superlinear before larger
scales.

## 2. Current-code map (verified at `a640d2b0`)

- **`FSM.GetCandidates(rel, minFreeBytes, n)`** — `internal/storage/fsm.go:71`:
  returns up to `n` block numbers with ≥ `minFreeBytes` free. It is a **linear
  scan over the per-relation page map** (`for blk, free := range pages`,
  `fsm.go:90`) — O(n) in the relation's page count, and over a `map`, not an
  array. That is the cost that grows with scale.
- Tests establishing current behavior:
  `internal/storage/fsm_test.go` — `TestFSMGetCandidatesBasic` (:99),
  `TestFSMGetCandidatesLargeRelation` (:181),
  `TestFSMGetCandidatesDoesNotMutateState` (:228).

## 3. PostgreSQL reference

- `src/backend/storage/freespace/freespace.c` +
  `src/backend/storage/freespace/fsm_internals.c` — PG's FSM is a **three-level
  tree of max-free-per-page bytes**; `fsm_search` walks from the root following
  the max-child pointer, so finding a page with enough space is O(tree height) ≈
  O(log n), not O(n). `RecordAndGetPageWithFreeSpace` combines the update and the
  search.

## 4. Target design

`GetCandidates` is a linear O(n) map scan today (§2). Replace it with a
max-tree descent mirroring PG's `fsm_search`: an upper-level array of per-range
max-free values guides the descent so the walk is O(log n) in the relation's
page count.

### Decision log

- **D1 — the complexity is already known: O(n) map scan (§2).** S0 is therefore
  not "O(n) vs O(log n)" but "quantify the constant and confirm the growth curve
  vs relation size" — the restructure (max-tree) is the expected fix; a
  micro-optimization (cache the last candidate, batch update+search) is the
  fallback only if the curve turns out shallow at realistic sizes.
- **D2 — mirror PG's three-level max-tree.** The proven design; do not invent a
  variant.

## 5. Invariants and failure modes

- **I1 — no false candidates.** A returned block must actually have ≥
  `minFreeBytes` when the caller tries to use it (concurrent inserts can consume
  space between search and use — PG re-checks on the page and retries; goopg must
  too). `TestFSMGetCandidatesDoesNotMutateState` guards purity.
- **F1 — stale FSM overstates free space.** After a page fills, the FSM must be
  updated or the next search keeps returning it; bound retries.

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S0 | measure | micro-benchmark the O(n) map scan vs relation size; quantify the constant + growth curve. | G-perf |
| S1 | fix | either constant-factor (cache/batch) or max-tree descent per S0. | G-race, G-tpch |
| S2 | perf acceptance | `-N` at scale 500 (and a scale-1000 spot-check): `GetCandidates` CPU share flat or falling with size. | G-perf |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| FSM candidate correctness | `internal/storage/fsm_test.go` | S1 |
| large-relation scaling | `TestFSMGetCandidatesLargeRelation` | S0, S2 |
| TPC-H spotcheck | `scripts/tpch-spotcheck.sh` | S1 |

## 8. Performance verification

Micro-benchmark across relation sizes (S0), then `run_rw50.sh` `-N` at scale
500: `GetCandidates` CPU share should not grow with scale.

## 9. Open questions

- **O-FSM-1** — Is `GetCandidates` even the right granularity, or should
  callers use a `RecordAndGetPageWithFreeSpace`-style combined update+search to
  halve the FSM traversals per insert?
