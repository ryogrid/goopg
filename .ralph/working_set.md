(idle — nothing in flight)

M0127-P3.1 is CLOSED and pushed. **S3 IS OPEN; P3.1 is its first landed task.**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P3.2` (batch build/probe: hashvalue-prefixed
`spillWriter` frames, per-batch inner/outer files, `HJ_NEED_NEW_BATCH` in
`nextLazy`, nbatch growth with capped give-up + WARNING; fold M0125-0032's
Q21 plain-EXPLAIN classification into its design loop). Bar: UNITS + DS05 +
RACE.**

Carry-over facts a next loop should not re-derive:

- **`internal/hashsize` is a LEAF package on purpose.** executor → planner,
  planner → executor NEVER, so the shared sizing rule sits below both. Do not
  "simplify" it into either package. `Choose` returns
  `Sizing{NBuckets, NBatch, SpaceAllowed, EntryBytes}`; only `NBuckets` has a
  consumer today (`joinOp.presizeLazyHash`). **`NBatch` is computed and
  ignored — wiring it IS P3.2.**
- **Four P3.1 ledger rows are P3.2's actual worklist.** (1) `hashJoinCost`
  still uses `rowsPerPage = 100` and has no batch-I/O term and no work_mem
  input at all (`costParams` has no memory field); (2) `avgVarBytes` is
  hard-0 because goopg collects no per-column width stat → NBatch biases LOW;
  (3) `FileBufferBytes = 8192` assumes a per-batch write buffer that
  `spillWriter` does not have (it writes frame + payload straight to the fd);
  (4) `maxPresizeBuckets = 1<<20` in `operators_join_agg.go` is a goopg-only
  cap that P3.2 should DELETE once NBatch bounds the table.
- **PG's `Assert(bucket_bytes <= hash_table_bytes/2)` does not hold in
  goopg** — a map slot is ~48 B vs PG's 8 B pointer, so "buckets alone
  exhaust work_mem" is reachable at small work_mem and `Choose` clamps the
  batch divisor instead of trusting it.
- **`Context.WorkMem == 0` means UNLIMITED**, not zero. `Choose(…, 0)` is
  NBatch=1 / SpaceAllowed=0; use `hashsize.EffectiveMemLimit` (→ 512 MiB)
  wherever a finite budget is needed to size a real allocation.
- **P3.1 moved no plan.** `m0127-p21-hashkeys` is still the PLAN baseline.
- **DS05 post-TIMEOUT restart hazard is still live** — the sweep dies after
  Q72 on a `systemd-run` scope-name collision. Recovery: `systemctl --user
  reset-failed`, `bench/tpcds/server.sh start sf05`, then
  `QUERIES="$(seq 73 99)" scripts/tpcds-sf05-regression.sh sweep`.
- **Do NOT `git stash`** in this tree (9 unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified.
  Tracking = `docs/design/0127-pg-shaped-join-search.md` §6 + fix_plan
  checkbox + README index status.

Gates run this loop: UNITS PASS; SPOT PASS (Q12=2 / Q13=35, 17.0 s, peak
11,461 MB); SMOKE via the commit hook; `make ralph-state-guard` OK (after its
own auto-repair of the previous loop's clean-exit marker). DS05/PLAN not run
— P3.1's bar is UNITS + SPOT and no plan surface was touched.

In-flight: none.
