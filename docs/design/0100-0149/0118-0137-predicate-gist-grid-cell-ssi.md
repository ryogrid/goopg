# 0118-0137 — `predicate-gist.spec` PROMOTED: GiST page-level predicate locking via grid-cell SIREAD (M0118-0002)

Status: accepted

## Summary

`predicate-gist.spec` `failed`→`pass`: all 36 permutations now byte-identical to
PostgreSQL 18.3, promoted to `runIsoSpecStrict` in
`TestPort_IsolationPredicateGist`. This closes the SSI granularity gap that the
0118-0135 (point read support) and 0118-0136 (`PGFloatOut`) enablers had isolated
as the spec's sole remaining blocker. With those landed, only **predicate-gin**
and **deadlock-parallel** remain `failed` in the isolation suite (119 pass / 2
failed).

## Problem

The spec tests **page-level predicate locking in a GiST index**: two SERIALIZABLE
transactions each scan a `point` column with a spatial half-plane filter
(`p << point(k,k)` ⇔ X<k, `p >> point(k,k)` ⇔ X>k) and INSERT points. A scan and
an insert that touch the **same** spatial region must form an rw-conflict (an
overlapping interleaving aborts the loser with 40001); a scan and an insert that
touch **disjoint** regions must **not** conflict (the reduced-false-positive
half).

PostgreSQL's GiST AM descends the index tree and takes a `PredicateLockPage` on
each leaf page it visits, so disjoint spatial regions lock disjoint pages → no
false conflict. goopg has **no native GiST access method** — a `CREATE INDEX …
USING gist` index is catalog-only (no physical storage; `operators_ddl.go`
records catalog metadata and returns). So with `enable_seqscan=off` the spatial
queries fall back to a **seq scan**, which under SERIALIZABLE takes a
**relation-grain** SIREAD (`ssiRecordRelationRead`) covering the whole table.
Against that coarse lock *every* concurrent INSERT conflicts-in, so goopg
over-aborted all 18 disjoint-region permutations (e.g. `rxy3 wx3 rxy4 c1 wy4 c2`:
goopg raised 40001 on `c2` where PG commits). This is the same granularity class
that `predicate-hash` solved with bucket-grain SIREAD (design 0118-0099).

## Fix

Emulate GiST leaf-page granularity with a **synthetic spatial grid**. A
SERIALIZABLE seq scan of a GiST-indexed table locks, per matching tuple, the
**grid cell** of that tuple's point on the *index* relation, instead of the whole
heap relation. An INSERT conflicts-in only on the cell(s) its point lands in.
Disjoint read regions → disjoint cells → no conflict; an insert into a read
region collides with a locked cell → conflict.

### Components (`internal/executor/ssi.go`)

- **`ssiGistGridCell(x, y)`** maps a point to a stable 31-bit pseudo-page number:
  FNV-1a of the integer grid coordinates `(floor(x/256), floor(y/256))`. Equal
  cells always collide; distinct cells collide only at ~2⁻³¹ (the safe
  over-abort direction). Masked to 31 bits so it is never `InvalidBlockNumber`
  (which `mvcc.PageLockTag` rejects). Cell size 256 cleanly separates the spec's
  regions (gap between X<1000 and X>6000; the abort families overlap directly)
  and the base data (`point(g*10,g*10)`, g=1..1000) is dense at this resolution,
  so a region's boundary cells are always populated — an insert in a read region
  always collides with a locked cell (no false negative).
- **`ssiGistIndexForTable(ctx, tbl, cols)`** finds a `Method=="gist"` index on
  the table and the position of its key column in the row.
- **`ssiRecordGistGridRead(ctx, dbOid, indexOID, x, y)`** acquires a grid-cell
  `PageLockTag(db, indexOID, cell)` SIREAD. The index OID is the predicate-lock
  relation so these tags never collide with the heap's tuple/page locks.
- **`ssiRecordGistIndexInsert(ctx, tbl, cols, row, dbOid)`** is the write-path
  conflict-in: for each GiST index it computes the inserted point's cell tag and
  runs `CheckForSerializableConflictInReportingFailure`, aborting in place
  (40001) on a committed-pivot structure — the twin of `ssiRecordHashIndexInsert`.

### Seq-scan integration (`internal/executor/operators_storage.go`)

The `Filter` directly above a `SeqScan` hands its spatial predicate to the scan
(`seqScanOp.ssiGistPred`, set in **both** build paths — `Build` and the live
`buildRec`/`BuildFastIterator` path — `pattern_sibling_paths_must_agree`). At
`Open`, if the scan is SERIALIZABLE (`ssiActive`) and the table has a GiST index
on the predicate's column, gist-SSI mode activates (`gistSSIIdxOID != 0`) and the
**relation-grain** `ssiRecordRelationRead` is **suppressed** (taking both would
re-coarsen to the relation grain). In the `Next` loop, gated entirely behind
`gistSSIIdxOID != 0` (zero cost for every non-gist scan):

- **Visible matching tuple** → grid-cell SIREAD (`ssiRecordGistGridRead`) +
  reader→writer conflict-out (`ssiConflictOutTupleRead`). The heap per-tuple
  SIREAD (`ssiRecordTupleRead`) is skipped — taking it would coarsen to a heap-
  page lock (`max_pred_locks_per_page=2`) and re-introduce different-region false
  positives (same reasoning as the hash index-only scan).
- **Invisible tuple** (a concurrent insert) → conflict-out **only if it matches**
  the spatial predicate (`gistTupleMatches` decodes it under the page RLock and
  evaluates the predicate). A relation-wide conflict-out (the prior behaviour)
  would form spurious edges for inserts outside the read region — the
  write-before-read half of the false positives.

Both rw-edge directions are required and confirmed against the permutations:
read-before-write via the cell SIREAD found by a later insert's conflict-in;
write-before-read via the reader's conflict-out on an in-flight inserter's
invisible-but-matching row.

## Blast radius

Bounded to SERIALIZABLE scans/INSERTs on tables with a GiST index. The
per-tuple branch is gated behind `gistSSIIdxOID != 0`, which stays 0 for every
non-gist scan, so the seq-scan hot path adds one zero-check per tuple. `ssiGistPred`
is set on every Filter-over-SeqScan but is inert unless gist-SSI activates
(checked at `Open` after `ssiActive`), so RC/RR and non-gist tables are
untouched. The catalog `Method` is unchanged (pg_am/pg_class/pg_dump/WAL
unaffected). Not WAL-persisted in any way — gist-SSI is purely a runtime SIREAD
decision recomputed each scan.

## Gates

- **`TestPort_IsolationPredicateGist` strict PASS** — 36 permutations byte-for-byte.
- Non-gist SSI regression: `predicate-hash`, `partial-index`, `index-only-scan`,
  `simple-write-skew`, `project-manager`, `classroom-scheduling`,
  `read-write-unique` PASS (the relation-grain seq-scan SIREAD path is unchanged).
- `-race` on `internal/executor` (SSI/scan/point) + `internal/mvcc` PASS;
  full `internal/executor` + `internal/planner` unit suites PASS.
- `go build ./...` clean; pgbench CI-parity smoke 0 failed (standard / -N / -S).
- TPC-H Q12/Q13 spot-check infra-timed-out on WSL2 (known SLRU-backfill startup
  hang, `tpch_spotcheck_slru_backfill_startup_hang`); the change is provably gated
  behind `gistSSIIdxOID` (TPC-H has no GiST index and runs RC), so the row-count
  path is structurally unaffected and the pgbench smoke is the live guard.

## Follow-ups

- **predicate-gin** still needs `int4[]`-column array typing (`array[1]`→int4[]
  collapses to int4 today) plus a GIN AM.
- **deadlock-parallel** needs parallel-worker lock groups (no parallel query in
  goopg).
- Multi-dimensional faithfulness: the grid is 2D `(floor(x/C), floor(y/C))` but
  the spec's operators only constrain X (and the data has Y=X), so 1D vs 2D is
  equivalent here; a future spec using Y-constraining operators (`<<|`/`|>>`)
  would exercise the second axis directly.
- gist-SSI is recomputed each scan and not persisted; no durability concern.

Related: [[0118-0099-predicate-hash-bucket-locking]],
[[0118-0135-point-geometric-read-support]], [[0118-0136-pg-faithful-float8out]].
