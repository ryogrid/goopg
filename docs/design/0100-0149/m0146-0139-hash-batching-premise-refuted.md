# M0146-0139 — hash-join build batching (TPC-DS Q79): the premise was refuted

Status: done 2026-10-09 (no code). Parent: M0146-0014a.

## The filed premise

The M0146-0014a sweep read Q79's plans and reasoned as follows. The
`customer` Hash (100000 rows, about 9.8 MB) exceeds PG's default
`hash_mem` of 8 MB (`work_mem` 4 MB × `hash_mem_multiplier` 2). PG would
therefore split it into 2 batches, at about 7100, which would keep PG's
`customer_pkey` nested loop (5248.4) cheaper. goopg charges no batching
(startup 5104.0).

The proposed first step was to re-decide M0139-0007a's held-off spill arm,
`GOOPG_PG_HASH_TUPLE_SPILL_COST`. That arm ports PG's batching geometry
(`ExecChooseHashTableSize`, nodeHash.c) and the batch I/O charge in
`final_cost_hashjoin` (costsize.c).

## What was measured

- **The spill arm, re-measured.** Flipping it to default-on and running
  the fire set changed no plan at either scale (`FIRE-SET: fires=none` on
  both corpora). This reconfirms M0139-0007a's HOLD on the current tree.
- **Why it cannot move Q79.** Both clusters run with `work_mem = 512MB`:
  the PG reference on :65438 (`SHOW work_mem`) and goopg's own
  `postgresql.conf`. `hash_mem` is therefore 1 GB, and neither engine
  batches a 10 MB build. The trace confirms `pgbatches=1` for the
  `customer` hash. The premise's 8 MB `hash_mem` does not apply to the
  benchmark clusters.
- **Q79 at SF1** already matches PG on the current tree.
- **Q79 at SF0.25** (`[join-method, scan-type]`) comes down to two things,
  per the DP trace on the private clone:
  - *The probe cost.* goopg's `customer_pkey` nested loop exists
    (`nestloop.index`, total 29039.56) but prices each probe at 8.44,
    against PG's 4.63. That is the B8 index-probe ×2 multiplier
    (`indexProbeCostMultiplier`), whose fix waits on the owner's
    M0146-0068 option choice.
  - *A near-tie.* Without B8, goopg's nested loop would come to about
    24660. That is in the same range as PG's 24562.57, and as goopg's hash
    join at 24425.30.

## Outcome

- **No code change.**
- **The spill arm stays HOLD.** M0139-0007a's design doc already
  describes it; this re-measurement adds the zero-fire result on the
  2026-10-09 tree.
- **Q79 at SF0.25 is routed to B8** (M0146-0068, owner) and is ledgered
  as such.
