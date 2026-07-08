Task: M-NIGHTLY pgbench/nightly-reopen-20260709 (3rd occurrence of the
recurring btree corruption, AI-20260709-010336-082) — investigation
in-flight, root cause NOT yet fixed.

Files:
- internal/executor/operators_bt_index_check.go — landed (small, real fix):
  btIndexReportDetail now always includes the block number, not just when
  len(reports)>1. Committed.
- internal/access/btree/btree.go — NOT yet touched; `insertIntoBlock`
  (~lines 2115-2350) is the prime suspect (old-right-sibling relink,
  `oldNext`/`sibSlot`/`sibOp.Prev` around line 2148-2303).

Key symbols: BTree.insertIntoBlock, BTree.splitMu (per-*BTree-instance, does
NOT serialize across connections — each backend opens its own *BTree per
statement per a prior loop's finding), storage.Pool.pinW/contentMu (the only
real cross-connection serialization for a shared page).

Hypothesis/Findings:
- Repro confirmed at HEAD: isolated port 5533, `pgbench -i -s 50` once, then
  `pgbench -c 100 -j 20 -T 25 -P 5` twice — 0 failed transactions (does not
  reproduce the runtime abort in this short window, as with every prior
  reopen's 1st loop).
- Post-run `bt_index_check('pgbench_accounts_pkey'::regclass, true)` DOES
  find real, ON-DISK, persistent corruption (verified via direct file read
  with the server stopped, not a live buffer-pool artifact): "left link/
  right link pair ... not in agreement". This is a DISTINCT symptom class
  from 2026-07-07 (bufmap tombstone / flushBatch stale-tag) and 2026-07-08
  (amcheck duplicate-key false positive) — both already fixed and NOT the
  cause here.
- Localized to block 678 (leaf, level=0): on-disk Prev=677, but the real
  sibling chain is 677 --Next--> 15798 --Next--> 678 (15798.Prev=677,
  15798.Next=678). 678's left-link was never relinked to 15798 when 15798
  was spliced in — matches upstream amcheck's own documented "classic
  example" of a non-atomic split leaving the ORIGINAL right sibling's
  left-link stale (postgres/contrib/amcheck/verify_nbtree.c:1079-1088,
  `bt_recheck_sibling_links` doc comment).
- Hand-traced `insertIntoBlock`'s existing relink code (oldNext captured
  once under blk's continuously-held pinW; sibSlot pinW'd + sibOp.Prev
  written later) and could NOT find an inspection-only defect — matches
  this whole investigation's pattern where only LIVE INSTRUMENTATION (not
  code reading) has ever found the real bug (see fix_plan.md's 14-17-loop
  writeups for the 2026-07-07/08 threads).
- LD_LIBRARY_PATH gotcha (independently hit, matches AI-...-001/SASL item's
  diagnosis): `postgres/local_install/bin/psql`/`pgbench` need
  `LD_LIBRARY_PATH=$PWD/postgres/local_install/lib` or they resolve the
  SYSTEM libpq.so.5 (missing PQsendPipelineSync) instead of the bundled one.
  Export this alongside PATH for every manual repro from now on.

Next step: add a `DebugTraceSiblingRelink`-style trace to `insertIntoBlock`
(matching the committed `DebugTraceBufmap`/`DebugTraceInserts` precedent —
off by default, cheap, kept committed), logging (splitting blk, rightBlk,
sibBlk, sibOp.Prev before/after) keyed by block, then re-run the PRESERVED
repro below and filter for block 678 (or whatever block the next repro
attempt flags — corruption block number varies run to run).

**Do not re-run pgbench -i -s 50 from scratch** — the exact corrupted data
dir from this loop is preserved at `tmp/perf-optimize/reopen3-data` (server
stopped cleanly, gitignored, ~481M). To resume: start goopg against it on
port 5533 (`GOOPG_CG_UNIT=<new-unique-name> scripts/goopg-test-run.sh
./bin/goopg start -D tmp/perf-optimize/reopen3-data --listen 127.0.0.1:5533`),
instrument, restart as needed. If it's been cleaned up by the time this
resumes, regenerate via the recipe above.

Gates run: go build ./... clean; go test ./internal/executor/...
./internal/amcheck/... PASS; scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33).

In-flight: none (no gate/process left running; goopg on 5533 stopped
cleanly, data dir preserved on disk as noted above).
