Task: M0131-S30.9 — CLOSED as NOT-A-DEFECT (evidence retracted). S30.8 UNBLOCKED.

**Read `docs/design/0131-0024` §7 before touching anything in S30.**

The big result of loop #142: **S30.9's live-server data-loss finding was a
HARNESS artefact and is retracted.** All three probe scripts backgrounded the
server with `&` then called a bare `wait`, which waits on ALL background jobs
including the server (never exits). Whether it was a barrier depended on
whether `scripts/goopg-test-run.sh` stayed in its job (→ hangs forever; seen
twice this loop as a fake multi-hour "stuck in the measurement query" while the
load had finished in <1 min) or detached into a systemd scope (→ returns while
clients are STILL inserting → counts a partially-loaded table). Fingerprint of
the bad run: `rows=75922` + `heap_missing=6328` implies 2250 duplicate rows
under a PRIMARY KEY.

Files: `analysis/lostrows-concurrent-insert.sh` (hardened gate),
`analysis/lostrows-postmortem.sh` + `analysis/lostrows-ctiddump.sh` (NEW
evidence collectors), `docs/design/0131-0024-live-server-committed-insert-loss.md`
(§7 retraction), `docs/design/README.md`, `.ralph/fix_plan.md` (S30.9 [x],
S30.8 unblocked), `.ralph/deferral_ledger.md`.

Key symbols: none in product code — this loop changed NO engine code.

Findings: 5/5 clean runs at the exact failing scale (80000 rows, 8 clients):
`rows=80000 heap_missing=0 index_unreachable=0 heap_dupes=0`. Also settled for
free: eviction was NEVER in play (`shared_buffers_slots=16384` vs ~1000-page
working set) and the failing run logged ONE checkpoint, at startup before the
load — so neither the flush/victim path nor the checkpointer could have caused
the reported loss. The old `NOT EXISTS (… WHERE b.id+0 = g)` anti-join metric
is retired: it never finishes at 80000 rows and returned exactly `6328` on two
runs whose `count(*)` shortfalls differed.

Next step: work **S30.8** as originally filed (non-HOT heap-update replay arm in
`internal/wal/recovery.go` + its `pd_lsn` skip guard; `pgbench_accounts` is
fillfactor 100 so most updates move the tuple across pages). Gate:
`RUNS=2 bash analysis/crashprobe30.sh` must print `OVERALL: PASS`. Before
reading its crash arm, run the hardened `analysis/lostrows-concurrent-insert.sh`
once — and note the residual ledger item: crashprobe30's atomicity invariant has
still never been shown to PASS on a no-crash control.

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(cached); `make ralph-state-guard` PASS (auto-repaired the previous loop's
completed marker); commit-hook pgbench smoke PASS; the retracted probe 5/5 PASS.

Nightly triage: `ci/logs/action-items.md` still run `20260811-014635`
(AI-…-001..012) — all 12 already filed under M-NIGHTLY (003..011 share one
batched row); nothing new. Note 5 orphaned `TestPort_RegressSuite` goopg servers
(13-22 h old, ~430 MB) are still resident; `kill` of them is classifier-denied.

In-flight: none.
