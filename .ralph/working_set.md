Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. This loop's actual work was recovering and
closing out the 10th loop's investigation, which had been fully coded
and written up in fix_plan.md but was cut short by a usage-limit
interruption BEFORE the deferral ledger row was written or anything was
committed (progress.json showed a stale `"status": "api_limit"` snapshot
and git had 5 modified files sitting uncommitted at loop start). Verified
the uncommitted diff was complete and correct, re-ran all gates fresh,
wrote the missing 10th-loop deferral_ledger.md row (row 604, matching the
existing 9-row table format), and committed everything as f514ac76.

Files (all already committed, nothing in flight): internal/storage/
io_trace.go (NEW `IOTraceEventsForTag(tag) []ioTraceEvent`, sorted by
completion time T — reuses the pre-existing GOOPG_IO_TRACE=1 content-hash
tracer from commit 8ebb71cd instead of building a new relFile-local
counter). internal/amcheck/verify_nbtree_realtree_test.go (new syscall-
level diagnostic block in TestVerifyBtreeEngineSilentOnRealConcurrentContended
cross-referencing each lost entry's block against IOTraceEventsForTag;
t.Skip message updated with the 10th-loop finding). .ralph/fix_plan.md +
.ralph/deferral_ledger.md (10th-loop row/update, already written by the
interrupted session, verified accurate and left as-is).

Key symbols for next step: internal/access/btree/btree.go's `pinW`/
`unpinW` (~line 1365-1377) — the SOLE choke point every leaf/internal
page mutation passes through (acquires/releases storage.Slot.contentMu
via `s.Lock()`/`s.Unlock()`). No instrumentation currently exists AT this
layer; all prior loops' snapshots (OnFlushSnapshot/OnBlockReload) are one
level higher (flush-to-disk / reload-from-disk), and the 10th loop's
IOTraceEventsForTag is one level lower (the OS syscall). The still-unfound
bug must live in the gap between them: a specific pinW-held critical
section that silently drops an already-inserted item while leaving the
page's item COUNT unchanged (observed: same itemCount=379 across two
consecutive flushes of the same block, entry present then absent, item
count identical — ruling out any split/dedup/rewrite event, all of which
change item count and are already independently cleared).

Hypothesis/Findings: 10th loop definitively REFUTED hypothesis (b) (a
stale/superseded write clobbering a newer one) at the smgr/syscall layer:
cross-checked 60863 real ReadAt/WriteAt completion events (GOOPG_IO_TRACE=1)
across 38 implicated blocks / 49 lost entries in one repro run — every
single postRead hash exactly matched the immediately-preceding postWrite
hash for that (relFile, block); zero stale-write mismatches. Combined with
the 8th loop's proof that flush-side WriteBlock always durably wrote
whatever was in memory, this clears the ENTIRE storage/smgr layer
(bufpool eviction/reload/flush + the OS read/write path) as a suspect.
The loss is now conclusively an IN-MEMORY content bug: something inside a
pinW-held critical section (insert/split/dedup, all already covered by
DebugTraceInserts/RewriteLogEvent at the "what happened" level but NOT at
a "did the page's bytes silently lose this item under this exact lock
hold" level) drops the entry without changing item count. Manual
inspection of bufpool.go's pinLoad/tryPinSlot/claimVictim tag-publish
ordering found no obvious gap but this was inspection only, not
instrumented — do not treat it as cleared.

Next step: instrument pinW/unpinW (btree.go ~1365-1377) with a
before/after content-hash + itemCount pair around the hold (mirroring
io_trace.go's HashPage on slot.Page(), bracketing the FULL lock hold, not
just entry/exit of specific callers) so a targeted repro can identify,
for one implicated block across the exact Seq window between a "good"
flush (entry present) and the next flush of the same block (entry
absent, same item count), which SPECIFIC pinW/unpinW hold is where the
byte content changed from including the entry to excluding it. Once that
hold is identified, map it back to its caller (insertIntoBlock's fast
no-split path, tryInsertOnCachedRightmost — already proven dead in a
DIFFERENT investigation thread per the AI-20260706-201855-001 ledger
chain, so re-verify liveness for THIS repro rather than assuming — or
some other write site) to find the actual logic bug. Do NOT re-open
claimVictim, the fast-path insert sites' own correctness (already traced,
just not yet hash-bracketed), the split/dedup-rewrite path, the
clean-eviction path, the dirty-flush write side, or smgr.go's
readBlock/writeBlock/relFile.mu serialization — all six conclusively
cleared across 10 loops. Do NOT re-litigate hypothesis (b) — refuted with
hard syscall-level evidence (60863/60863 events, zero mismatches).

Gates run this loop (all PASS): go build ./... (clean); go vet
./internal/storage/... ./internal/amcheck/... ./internal/access/btree/...
(clean); go test ./internal/storage/... ./internal/access/btree/...
./internal/amcheck/... (PASS, target test skipped by design); pre-commit
hook's CI-parity pgbench smoke (PASS, ran automatically on `git commit`);
make ralph-state-guard (pending — run before finishing this loop, see
status block).

In-flight: none. No background processes left running. All work from
this loop is committed (f514ac76). The 11th loop's contentMu-bracketing
instrumentation was scoped but NOT started this loop (this loop's actual
task was recovering + committing the 10th loop's orphaned work, which is
a complete, self-contained unit — starting new instrumentation now would
violate the one-task-per-loop rule).
