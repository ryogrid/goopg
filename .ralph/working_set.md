Task: M0134-0045 (misc.sql, status `failed`) — sizing complete, NOT yet fixed.
Research-only loop (no code changes landed).

Files: none touched yet this loop (`.ralph/fix_plan.md` M0134-0045 row still
the stub description — needs updating once the fix is scoped, not just sized).

Key symbols: `internal/access/nbtree/btree.go` — `mustInsertItemSorted`
(~line 3448, the "pre-verified space" writer that panics), called from the
split left/right refill loop inside `insertIntoBlock` (~lines 2960/2964) and
from the dedup-recovery rewrite path (~line 2847). Split-point estimation
suspects: `byteAwareSplitLoc` (~2894), `compactRawSize`, `CheckPGBTThirdPage`.
Panic call site in the executor: `maintainUniqueIndexesForInsert`
(`internal/executor/operators_storage.go:7847-7883`, misleadingly named —
maintains ALL indexes, not just unique ones) → `BTree.Insert` (btree.go:2585).

Hypothesis/Findings: `misc` regress case is NOT a "many small mismatches"
diff like prior M0134 files — it's a single hard SERVER CRASH partway through
the script. `UPDATE onek SET unique1 = unique1 - 1;` (the second full-table
UPDATE, ~1000 rows, driven by prerequisite `create_index.sql`'s
onek_unique1/onek_unique2 btree indexes) panics with `storage: not enough
free space in page` inside the split-refill path. Every downstream statement
(COPY fixtures, postquel-function SELECTs) never executes → ~340 of 361 diff
lines are pure fallout from the dropped connection, not independent bugs.
0 `^+ERROR`/`^-ERROR` (not an error-text mismatch case). This is a REAL,
unfixed defect — NOT covered by the closed ledger row root-0040
(2026-08-06), which hardened the *no-split fast-path* variant of this same
panic family (`tryInsertNoSplit`/`tryInsertOnCachedRightmost` + top-level
no-split attempt) and made `insertIntoBlock` panic-safe via `wlatch` (so this
crash is a clean dropped connection, not a wedged cluster) — but did NOT
touch the split-refill (`mustInsertItemSorted` at 2960/2964) or
dedup-recovery-rewrite (2847) call sites, which still assume pre-verified
space and still panic. Distinct from the WAL-redo-path panics with the same
string (root-0032/0033/0034, S30.3, in internal/wal/recovery.go — different
subsystem, don't conflate).

Next step: a researcher follow-up round is IN FLIGHT (SendMessage sent to
agent id `a714b8169f952329c`, asking it to (1) build a standalone throwaway-
server repro of the split-refill panic outside the full regress harness for
faster iteration, (2) instrument to capture actual vs predicted free space at
the panicking insert, (3) diagnose which of
byteAwareSplitLoc/dedup-posting-tuple-accounting/CheckPGBTThirdPage is wrong
and by how much, (4) report exact fix location + shape — still WITHOUT
implementing). **This may not have landed a reply before this loop ended**
(async SendMessage continuation, not a synchronous Agent() spawn — per the
Headless Execution Reality section this project's Ralph loop cannot rely on
background notifications arriving). Next loop: check for a reply first
(SendMessage to the same agent id/name asking for status, or re-spawn a fresh
researcher pointed at this file + a new standalone-repro brief if the process
died). Once the exact split-sizing bug is pinned, write it up as a
docs/design/ addendum (nbtree split-path design if one exists, else a new
root-XXXX doc) and brief an `implementer` for the actual fix — this is core
storage-engine correctness, do NOT skip the design step given the risk.
Do NOT mark M0134-0045 "PARKED" with a landed bucket yet — nothing is fixed;
if the split-refill bug can't be pinned within ~2 more research rounds,
consider parking file-level (misc.sql alone can't progress past 0/1 until the
crash is fixed) and moving to M0134-0046, but the underlying nbtree bug still
needs its own tracked task either way (it likely also blocks other bulk-
UPDATE-heavy files like update.sql/vacuum.sql — worth grepping other parked
M0134 files' crash logs for the same panic string before deciding scope).

Gates run this loop: `scripts/pg-regress-runner.sh --verbose misc` (via
researcher) — FAIL, 361-line diff, root cause identified as server crash
(not yet fixed). No go build/go test run this loop (no code changed).
`make ralph-state-guard` — repaired a stale progress-status marker, then
PASSED clean.

Delegation: researcher agent `a714b8169f952329c` — round 1 complete (sizing +
bucket report, done inline); round 2 (standalone repro + exact diagnosis)
sent via SendMessage this loop, reply status unknown (see Next step). No
handoff dir written yet (nothing durable enough to justify one until the
diagnosis lands — will create `tmp/ralph-handoffs/M0134-0045-nbtree-split-
refill/` once briefing an implementer).

In-flight: researcher round-2 SendMessage to agent `a714b8169f952329c` (see
above) — no local process/PID to reap (it's a subagent turn, not a Bash bg
task); if a reply arrives as a task notification, use it directly; otherwise
treat as lost and re-spawn fresh next loop with the same ask, citing this
file. No server left running (researcher instructed to clean up any it
starts). No uncommitted code changes — tree is clean except this file plus
the routine `.ralph/progress.json`/`ci/logs/*.log` bookkeeping churn.
