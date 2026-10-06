# M0146-0056 — the catalog-index "short read" wedge and the regress runner's shared datadir

Status: done 2026-10-06. Cause attributed to a harness collision, fixed in
`scripts/pg-regress-runner.sh`. The goopg storage hypotheses were tested and
did not reproduce. The GIN/BRIN short reads are filed separately as
M0146-0069.

## Symptom (2026-10-04, M0146-0009o)

- One run of the 18-case planner regress set failed
  `CREATE TABLE dupindexcols AS …` in `create_index` with:

  ```
  DDL catalog sync: pg_class_relname_nsp_index: insert leaf blk 22
  sys btree 2663: pin leaf blk 22: short read at block
  ```

- Every later CREATE in that run failed the same way.
- Two reruns of the same binary were clean.

## Two different "short reads"

The evidence counted every `short read at block` across the run's diffs,
which mixed two classes.

1. **Deterministic, every run: GIN and BRIN.**
   - goopg registers gist/spgist/gin/brin indexes in the catalog only, with
     no physical storage.
   - The planner's bitmap path builder (`pathbitmap.go`) still offers a
     Bitmap Index Scan on them, and reading the absent index file fails.
   - `create_index`'s `array_index_op_test` COPY fails on NULL array
     elements, so the GIN index is empty and the `i = '{47,77}'` scan
     errors. `stats`' `brin_hot_3` scan fails the same way.
   - Five occurrences in every run; filed as **M0146-0069**.
2. **Once only: the catalog-index wedge.** This is the subject of this
   task.

## Reproduction attempts (goopg side)

- The 18-case set ran four more times on a fresh datadir with HEAD's
  binary. Only the five class-1 short reads appeared; there was no
  `DDL catalog sync` failure.
- No catalog-index or storage code (`sys_catalog_btree_multilevel.go`,
  `sys_catalog_index_insert.go`, `smgr.go`, `bufpool.go`) changed after
  the incident. A goopg defect there should therefore still fire.
- Concurrent DDL stress on a throwaway server: two sessions × 400 CREATE
  TABLE. There were no errors, and the name index found every table
  before and after a restart.
- Storage reasoning:
  - `smgr` rejects a read at or past its cached `nblocks`.
  - `Pool.PinNew` extends the file synchronously, so a referenced block
    can only be missing if the file shrank or was replaced.

## The harness collision (cause)

`pg-regress-runner.sh` auto-starts every server on port 15435, under the
fixed systemd scope `goopg-regress-runner`, with datadir
`<repo>/tmp/regress-goopg-data`. Its exit trap stopped that scope and ran
`rm -rf` on the datadir on every exit. That included the exit-2 refusal
of a second runner whose port check found the first runner's server.

Experiment (evidence file below): with runner A mid-run, start runner B in
the same tree. B refuses (rc 2), and its trap kills A's server and deletes
A's datadir. A aborts with "connection lost".

Under a different interleaving the old datadir is removed and a fresh one
initialised (B passes the port check before A listens) while A's server
survives. Files A opens afterwards are then the new, small initdb copies,
but A's buffer pool still holds the old metapage and internal pages of
2663. They route to leaf 22 past the new file's end, giving `pin leaf blk
22: short read`. Every later insert takes the same route, so every later
CREATE fails. That is the incident's signature.

## Fix

- A per-port exclusive lock, `/tmp/goopg-regress-runner-<port>.lock`
  (`flock -n`), taken before anything is touched. It is shared by all
  worktrees, since the port and scope are global. A second runner is
  refused with no side effects.
- `cleanup()` releases only what this invocation created:
  - the server pid it recorded;
  - the scope, only after it started one (`OWN_SCOPE`);
  - the datadir, only after it initialised one (`OWN_DATADIR`).
- Re-run of the experiment: B is refused by the lock, and A completes all
  18 cases with its datadir intact.
- Follow-up (`ebfb403e5`, found by M0146-0058's regress A/B): the server
  inherits the lock descriptor, so the lock is held until the previous
  run's server has really exited. A back-to-back A/B pair was refused
  while that server was still exiting, so the runner now waits up to 120s
  (`flock -w 120`) before refusing.

## Not covered

- The incident itself is attributed, not observed. Neither the server log
  nor a concurrent invocation from that afternoon survives. The
  transcript of the session that ran it shows its own runner calls as
  sequential.
- Latent goopg hazard (filed as M0146-0070):
  `rebuildSysBtreeWithNewEntry` overwrites block 0 (the metapage) and
  existing internal pages before it extends the file for new tail pages.
  An error between the two leaves downlinks past EOF, the same signature.
  No witness reaches that error path today.

## Evidence

- `analysis/m0146/m0146-0056/repro-and-runner-collision.txt`: the four
  repro iterations, both runner experiments, and the concurrent-DDL
  stress.
- `analysis/m0146/m0146-0056/regress-create_index.diff`,
  `regress-runner.log`: the original incident.
