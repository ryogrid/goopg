# 08-03 — C3 residual: migrate UPDATE-probe RangeScan callers to LP_DEAD kill collection

status: design · date: 2026-07-14 · base: `a640d2b0` · gates: G-race
(`./internal/access/btree/`), G-crash, G-waldump, G-tpch, G-perf →
[README](README.md)

## 1. Problem and numbers

C3 (btree LP_DEAD on-access cleanup) landed, but kill collection rides
`indexScanOp` only. pgbench `-N`'s UPDATE locates its row via a `RangeScan`
probe (`updateViaIndex`), and those callers do **not** collect kills, so dead
index entries accumulate until the no-space purge reclaims them. Evidence:

- 06-01: `pgbench_accounts_pkey` grew +166,830,080 B (one file doubling) in the
  120 s `-N` window; the 600 s soak (`s5c3_soak2_bcfd0ed9`) still showed one
  doubling = 43.6 B/txn, 14.9× below the pre-C3 baseline but not zero.
- 07-01: at scale 500 the pkey doubled again (+832,487,424 B in 120 s), and
  07-02 flags this as **costly under buffer pressure** — a 1.6 GB pkey no longer
  co-resides with the 2.2 GB heap, so dead entries directly raise the miss rate
  (it is no longer just disk waste).

## 2. Current-code map (verified at `a640d2b0`)

- **`BTree.RangeScan(lo, hi, fn)`** — `internal/access/btree/btree.go:3165`, and
  **`RangeScanWithPos`** — `btree.go:3161` (the pos-carrying variant); both
  funnel through the shared `bt.rangeScanPos` (`btree.go:3162`/3166).
  `updateOp.updateViaIndex` calls `RangeScan`
  (`internal/executor/operators_storage.go:3781` — the UPDATE probe,
  `RangeScan.func2` in the 07 profile). Neither collects LP_DEAD kills today.
- C3's kill collection is wired into `indexScanOp` (the landed path); the 9
  other `RangeScan` callers (UPDATE/DELETE probes, FK checks) are the
  deferral-ledger residual (project memory `perf-optimize3-dash-c2-c3-landed`:
  "migrate the 9 other RangeScan callers to kill collection").

## 3. PostgreSQL reference

- `src/backend/access/nbtree/nbtutils.c` — `_bt_killitems` marks index tuples
  `LP_DEAD` when a scan finds the heap tuple dead; PG does this for **all** index
  scans, including those backing `ExecUpdate`/`ExecDelete` row location, not just
  read scans (`nbtree.c` drives the scans that call it).

## 4. Target design

Give the `RangeScan`/`RangeScanWithPos` probe path the same kill-collection hook
`indexScanOp` uses: when the probe's callback observes that the located heap
tuple is dead (already the case for a superseded UPDATE target — the old version
is dead after the update), record the index entry's TID in the page's kill list,
and let the existing C3 on-access purge reclaim it.

### Decision log

- **D1 — reuse the landed C3 kill-list mechanism, do not add a new one.** The
  purge/eviction machinery exists; only the *collection* call site is missing on
  the probe path. This is a call-site migration, not new infrastructure.
- **D2 — scope to the UPDATE/DELETE probes first.** The pgbench-visible residual
  is the UPDATE probe (`updateViaIndex`); FK-check probes are lower volume.
  Migrate the UPDATE/DELETE probes in S1, the remaining callers in S2.
- **D3 — kills must respect the same visibility rule C3 established** (only mark
  entries whose heap tuple is dead to *all* snapshots — the LP_DEAD tolerance
  tested by `TestLPDeadInvisibleToSearchAndRangeScan`), or a live entry could be
  hidden.

## 5. Invariants and failure modes

- **I1 — never kill a live entry.** Collection uses the same
  all-snapshots-dead predicate as the landed C3 path; `TestLPDeadInvisibleTo…`
  is the guard.
- **F1 — kill during a probe that is itself part of a writing txn.** The UPDATE
  probe runs inside the updating transaction; marking the *old* version's index
  entry LP_DEAD is safe (that version is being superseded), but marking the
  *new* entry must never happen. Bound collection to entries pointing at the
  pre-update tuple.
- **F2 — WAL/standby.** If user-btree LP_DEAD hints are canonically replicated
  (open question O-C3-2 in the original C3 doc), the probe-path kills need the
  same treatment as scan-path kills; if not, they are local hints. Settle
  against the C3 doc before S1.

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | UPDATE/DELETE probe kills | wire the kill hook into the shared `rangeScanPos` path (reached by `RangeScan`, used by `updateViaIndex`, and `RangeScanWithPos`) for the UPDATE/delete probes; the pkey growth in `-N` should flatten. | G-race, G-crash, G-tpch |
| S2 | remaining RangeScan callers | FK-check and other probes. | G-race, G-tpch |
| S3 | perf acceptance | 600 s soak at scale 100 + a scale-500 `-N`: pkey growth per txn should approach 0; scale-500 miss rate should improve. | G-perf |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| LP_DEAD visibility tolerance | `internal/access/btree/lpdead_tolerance_test.go` | S1 (guard) |
| RangeScan correctness | `internal/access/btree/btree_test.go` (`TestRangeScan`) | S1 |
| pgbench `-N` soak growth | `run_rw50.sh` + soak | S3 |
| TPC-H spotcheck | `scripts/tpch-spotcheck.sh` | S1, S2 |

## 8. Performance verification

600 s `-N` soak at scale 100 (the metric C3 exists for): pkey file should not
double after warm-up; per-txn growth → ~0 (from 43.6 B/txn). Scale-500 `-N`
re-run: the +832 MB/120 s doubling should not recur, and the miss rate (07's
`pinSlow` reload wait) should drop.

## 9. Open questions

- **O-C3M-1** — Exact set of the "9 RangeScan callers": enumerate at
  implementation time (UPDATE, DELETE, FK check, MERGE, …) and confirm each is
  safe to collect kills from.
- **O-C3M-2** — Standby replication of probe-path LP_DEAD hints (inherits the
  original C3 O-C3-2).
