# Working set — M-NIGHTLY run 20260817-011734 DRAINED (all 6 items closed)

**Task:** M-NIGHTLY nightly triage, item **AI-20260817-011734-001**
(`race/internal/initdb`). Selected per the Current Priority banner (M-NIGHTLY is
the standing highest-priority obligation). **Closed STALE — verification only,
zero code change.**

**Finding:** the recorded nightly failure was `panic: test timed out after 45m0s`
(`FAIL github.com/goopg/goopg/internal/initdb 2700.056s`) — a whole-package
timeout from running 605 `Init()`-heavy tests as ONE `go test -race` binary. It
was **never a data race**: the goroutine dump showed only a normal in-progress
`pglz.Compress`. The run forked before the race-shard fix `83dd7ae8`
(`git merge-base --is-ancestor 83dd7ae8 HEAD` → IN-HEAD now).

**Verification at HEAD:** `make race-gate RACE_TIMEOUT=45m RACE_SHARD_ONLY=1`
→ 4/4 shards PASS (152/151/151/151 = 605 tests, matching `go test -list`),
per-shard 748.6 / 726.7 / 776.6 / 873.4 s, **wall clock 15m52s** — beats the
fix's ≈19m56s estimate, well inside the 45m budget. Useful datum for future
loops: this gate costs ~16 min, not ~45.

**Files:** `.ralph/fix_plan.md` only (item ticked with the evidence; run header
marked DRAINED).

**Gates run:** race-gate shard-only PASS (see above). No units/tpch-spotcheck —
bookkeeping-only change, no production code touched; the pre-commit pgbench smoke
still runs on the commit.

**Deferral ledger:** no row. Nothing PG-semantic was left unimplemented.

**Next step:** M-NIGHTLY run `20260817-011734` is fully drained (6/6). No
unchecked `## AI-` item remains in `ci/logs/action-items.md`. The banner therefore
routes the next loop to **M0134** (regress-sql `failed`/`not-tried` digestion) —
after the unconditional re-read of `ci/logs/action-items.md`, since a newer
nightly run may have landed. NOTE: the next nightly that starts *after*
`83dd7ae8` is the one that validates the sharding fix in CI; if it still reports
`race/internal/initdb`, that item is NOT stale and needs real triage.

**Delegation:** `tmp/ralph-handoffs/m-nightly-20260817-001-race-initdb/`
(tester DONE, 1 round; gate output in `race.log`).

**In-flight:** none.
