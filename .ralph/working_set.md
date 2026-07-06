Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
"item length mismatch keyLen=9 total=37" / "empty internal page"
recurrence investigation (NOT resolved; 5th consecutive investigation
loop, investigation-only, no functional change landed). MAJOR
REDIRECTION this loop — see Hypothesis/Findings below.

Files: none changed in the final commit besides `.ralph/fix_plan.md`
and `.ralph/deferral_ledger.md` (bookkeeping). Temporarily built and
FULLY REMOVED this loop: `bin/goopg-race` (`go build -race
./cmd/goopg`), a scratch data dir under `tmp/goopg-race-diag-<pid>`,
`/tmp/race_pgbench/*` logs, a `-race`-compiled test binary
(`/tmp/btree_race.test`). `internal/access/btree/
multi_writer_stress_test.go`'s `t.Skip` was removed then restored via
`git checkout --` (confirmed clean via `git status`/`git diff`).

Key symbols touched only for reading/verification this loop:
`internal/storage/bufpool.go` `Pin`/`pinSlow`/`pinLoad`/`tryPinSlot`/
`PinNew`/`claimVictim`/`evictVictim` (re-audited statically, no bug
found — eviction protocol looks correct: IO bit + gen bump in
claimVictim excludes concurrent re-claim, tag publish to bufmap always
happens AFTER content population in every caller checked).
`internal/access/btree/btree.go` `insertIntoBlock` (1421-1662),
`createNewRoot` (1818-1908), `finishSplit` (1727-1776) — all properly
re-`.Lock()` the slot returned by `PinNew`/`pinNewOrRecycled` before
writing btree content; no missing-lock write site found by static
read. `internal/storage/arena.go` (`newArena`/`slot`) — verified no
aliasing/overlap bug (each slot gets a disjoint, cap-bounded
`BlockSize` sub-slice). `internal/access/btree/posting.go` —
`appendTIDToPosting`/`promoteSingleToPosting` flagged `unusedfunc` by
`go vet` during this loop's test runs; NOT yet investigated whether
that's dead code or a real gap in dedup-consolidation reachability.

Hypothesis/Findings: **The last 4 loops' central hypothesis (a
genuine "unlocked write" data race in the buffer pool / btree layer,
"confirmed" via custom `GOOPG_PINTRACE` instrumentation) is now in
serious doubt.** This loop ran the SAME fast repro
(`TestMultiWriterStress_M0055_Phase_C`, `t.Skip` removed) ~1180 times
total this session — 100+300+500 in-process `go test -race -count=N`
runs, 80 separate-process invocations of a `go test -c -race`-built
binary (matching the earlier loops' own "loop `go test -count=1`"
recipe), and 200 plain (non-race) runs — on a 16-core box. **Zero
failures, zero race-detector reports**, across all of them. This
directly contradicts the previous loop's claim of a reproducible
unlocked-write race (that capture may have been an artifact of the
custom trace instrumentation's own synchronization, or the bug is
real but far rarer than the "~100% at -count=200 with tracing" claim
suggested — a Heisenbug specific to that instrumentation's own
overhead shape, not reproduced by -race's differently-shaped
overhead).

Per fix_plan rule 3 ("re-run the item's repro at HEAD before
investigating"), then ran the ACTUAL authoritative nightly repro:
`REPO_ROOT=$PWD RUN_DIR=$(mktemp -d) bash
ci/batch/stages/stage-pgbench.sh` (s=50 c=100 j=20 T=180). **This
still fails immediately and reliably** — dozens of clients hit `btree:
item length mismatch keyLen=9/keyLen=0 total=37` within ~30s of the
very first TPC-B workload starting. Built `bin/goopg-race` (`go build
-race -o bin/goopg-race ./cmd/goopg`), manually loaded a matching
scale=50 pgbench dataset (a scale=5 attempt first did NOT reproduce in
60s — very likely because it fits entirely in the buffer pool and
never exercises `claimVictim`/`evictVictim` eviction at all — scale
matters for reproduction), then ran the real TPC-B workload (`pgbench
-T 80 -c 100 -j 20 -P 5`) against the race-instrumented server with
`GORACE="log_path=... exitcode=0 halt_on_error=0"`. **The corruption
reproduced again** (25 client aborts, `keyLen=47460`/`keyLen=0
total=37`) but **the race detector logged ZERO "DATA RACE" reports**
for the entire run, despite Go's race detector being known to catch
syscall-buffer writes too (`os.File` methods wrap reads/writes with
`runtime.RaceWriteRange`). A clean -race run through an ACTUAL, live
reproduction of the real bug is strong evidence this is **not a
classic memory data race** — it's a logic bug in code that IS
correctly synchronized (right lock held, wrong bytes/offsets
computed), not a torn/unsynchronized write.

Combined with never having reproduced via the pure-insert, disjoint-
key btree-only unit test (0/1180 this session), the working theory is
now: **the real bug requires the full heap+index+MVCC stack under
UPDATE-heavy contention on tiny, heavily-shared tables** —
`pgbench_branches` (50 rows at scale=50) and `pgbench_tellers` (500
rows) are hammered by all 100 concurrent clients every transaction.
Per `[[goopg_no_hot_update_index_reeval]]` memory, goopg has NO HOT
update — every UPDATE inserts a brand-new index entry — so TPC-B's
per-transaction UPDATE on these tiny tables produces a rapid-fire,
duplicate-key-heavy insert/thrash pattern into a handful of btree leaf
pages that `TestMultiWriterStress_M0055_Phase_C`'s 32-writer/disjoint-
key/insert-only workload (btree.go — no updates, no deletes, no
VACUUM, no shared-row contention) structurally cannot model.

Next step: **stop chasing `TestMultiWriterStress_M0055_Phase_C`** —
it has not reproduced anything in 1180 attempts across 3 repro styles
this session. Instead use the manual pgbench-against-a-`-race`-server
recipe this loop verified works and is CHEAP (~15 min total: ~5 min
build+init, ~11 min load at s=50, ~80s workload, fails on the first
attempt):
```
go build -race -o bin/goopg-race ./cmd/goopg
./bin/goopg-race init -D <datadir>
GORACE="log_path=<path> exitcode=0 halt_on_error=0" \
  ./bin/goopg-race start -D <datadir> --listen 127.0.0.1:<port> &
pgbench -i -s 50 -h 127.0.0.1 -p <port> -U postgres postgres
pgbench -T 80 -c 100 -j 20 -P 5 -h 127.0.0.1 -p <port> -U postgres postgres
```
With a live failing server, instead of more -race iterations (already
shown fruitless), dump the actual corrupted page's raw line-pointer
directory + the specific item's byte range at the exact call site that
raises "item length mismatch" (grep btree.go) to see whether the
recorded length is wrong AT REST (confirms logic bug) — and check
whether `dedupConsolidate`/posting-list code
(`internal/access/btree/posting.go`'s `appendTIDToPosting`/
`promoteSingleToPosting`, flagged `unusedfunc` by `go vet` this loop)
is actually reachable/exercised on TPC-B's duplicate-heavy UPDATE
pattern, or is dead code masking a real dedup-consolidation gap.

Gates run this loop: `go build ./...` clean (before AND after full
revert); `go test -count=1 ./internal/storage/...
./internal/access/btree/...` PASS (baseline, after revert); `make
ralph-state-guard` OK. No executor/planner/codec changes landed, so no
TPC-H spotcheck required (investigation + docs only).

In-flight: none — the race-mode server, its data dir, the `-race`
test binary, and all `/tmp/race_pgbench` logs were fully removed
before this loop ended; `multi_writer_stress_test.go` restored via
`git checkout --` (confirmed clean). A separate, unrelated live
nightly CI batch (`ci/batch/run-nightly.sh`, started ~00:12 today) was
observed running concurrently during this loop — NOT touched, left
running (memory: nightly runs are designed to coexist via port
separation).
