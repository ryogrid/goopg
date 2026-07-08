Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. 12th loop on the reopen. Executed the 11th
loop's exact next step (wall-clock IO-trace postWrite-regression check)
and it CONFIRMED, with cross-layer validation, the racing-flush/stale-
reload mechanism that was left open. Committed as (see `git log -1`
after this loop's commit).

Files (all committed): internal/amcheck/verify_nbtree_realtree_test.go
(new `time` import; extended the existing GOOPG_IO_TRACE wall-clock
check with a postWrite-regression scan over `priorWrites`, refined
twice in-loop from a naive "any regression" check — 29 false-positive
hits, mostly ordinary page splits — to a magnitude-based split/non-split
classifier (`delta*4 > prev.LPCnt` = presumed split, ignored); skip
message appended with the 12th-loop finding). .ralph/deferral_ledger.md
+ .ralph/fix_plan.md (12th-loop rows).

Key symbols for next step: internal/storage/bufpool.go's claimVictim
(~1068, pin-count exclusion — cleared), evictVictim (~1527-1557, dirty
flush + bufmap-delete-before-flush, already fixed once for a DIFFERENT
symptom in commit 510615b4) and pinLoad (~1561-1572, the reload's own
direct s.contentMu.Lock()/Unlock() hold around ReadBlock — proven in the
11th loop to be a SEPARATE mutation site from bt.pinW/unpinW). The
confirmed gap is a missing exclusion between a slot's flush becoming
durable and a (possibly different-slot) concurrent reload of the SAME
tag being allowed to publish stale bytes.

Hypothesis/Findings: CONFIRMED, cross-validated at both the contentMu
(in-memory, 11th loop) and IO-trace (smgr/syscall, this loop) layers:
hypothesis (b) variant 2 — a cache-miss reload racing a legitimate flush
of the SAME block loads a STALE on-disk copy into the slot, silently
discarding the concurrently-flushed correct in-memory content; a
subsequent write on top of the now-stale slot then durably re-flushes
the loss. Concrete evidence this loop (fresh repro, missing entry
key=33666 TID={1,2883} at blk=377): reload-snapshot itemCount=399
(entry absent, stale) lands BETWEEN a correct flush-snapshot
(itemCount=401, entry present, seq=908124) and the next contentMu-hold;
the smgr IO tracer shows that correct flush as postWrite lpCnt=401 at
t=43.393270170s, followed just 1.9ms later by postWrite lpCnt=400
(=399 stale + 1 later insert on top of the stale reload) that clobbers
the correct disk copy. IMPORTANT tooling note for future loops: a naive
"any postWrite regression" check on this workload is mostly NOISE —
27/29 raw hits this loop were ordinary page splits (goopg splits by
BYTE SIZE not item count, so the surviving fraction is ~50.7%-53.8%, not
exactly half — a "half+2" cutoff still misclassifies them). Use a
magnitude cutoff (>25% drop = split) to isolate the real signal. Do NOT
re-open: claimVictim's pin-count exclusion, fast-path insert sites,
split/dedup-rewrite path, clean-eviction path, relFile.readBlock/
writeBlock's own r.mu serialization, or whether pinW/unpinW is the sole
contentMu choke point (it is NOT, per the 11th loop) — all conclusively
settled across 12 loops.

Next step: this is now a CONFIRM-COMPLETE, FIX-PENDING investigation —
stop instrumenting and land the actual synchronization fix in
storage.Pool's claimVictim/evictVictim/pinLoad (bufpool.go ~1527-1572).
Audit specifically: (1) can pinLoad's ReadBlock be in flight (or already
have returned, pending publish into the slot) for a tag WHILE a
different call is evictVictim-flushing a dirty slot for that SAME tag,
without any lock ordering that would serialize them? (2) can two
distinct slots transiently both claim the same tag during a flush-then-
reload handoff (the 9th loop already observed 3 different physical slot
indices serve one block's tag in a single run — re-examine that finding
now that the exact race is understood)? The likely fix shape: pinLoad
must re-validate (or block on) any in-flight/just-completed flush for
its target tag before publishing its ReadBlock result into the slot —
e.g. a per-tag flush-in-progress marker checked/waited-on inside
pinLoad's critical section, or extending the existing per-slot
contentMu hold to cover a tag-level check. After landing a candidate
fix, un-skip TestVerifyBtreeEngineSilentOnRealConcurrentContended (line
~762, `t.Skip(...)` in internal/amcheck/verify_nbtree_realtree_test.go)
and re-run it (foreground, several times for flake margin given this bug
is intermittent/timing-dependent) to confirm zero missing entries before
re-adding the skip and closing this task's checkbox in fix_plan.md.

Gates run this loop (all PASS): go build ./... clean; go vet
./internal/amcheck/... ./internal/storage/... ./internal/access/btree/...
clean; go test ./internal/storage/... ./internal/access/btree/...
./internal/amcheck/... PASS (target test re-skipped, confirmed both
skip-restore and its own short-circuit correctness); un-skipped manual
runs (temporary in-session edit, reverted before commit) reproduced and
confirmed the finding above 3 times across 4 GOOPG_IO_TRACE=1 runs;
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33); pre-commit hook's
CI-parity pgbench smoke PASS (ran automatically on `git commit`); make
ralph-state-guard OK.

In-flight: none. No background processes left running. All work from
this loop is committed. IMPORTANT process note: this loop discovered
that the test's `t.Skip(...)` at line ~762 is UNCONDITIONAL (not gated
by `testing.Short()`) — the actual repro body only runs when that line
is temporarily neutralized (wrap in `if false { ... }`, run, then
restore verbatim — do NOT `git checkout --` the file to revert, since
that discards ALL uncommitted edits in the file, not just the skip
toggle; this loop lost and had to redo its diagnostic-code edit that
way once). Any future loop touching this test must reproduce the same
temporary-unskip / restore-before-commit dance.
