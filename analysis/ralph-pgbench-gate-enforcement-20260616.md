# Ralph pgbench CI gate — process-gap diagnosis + machine enforcement (2026-06-16)

## Symptom

GitHub-Actions CI (`.github/workflows/test.yml`, `workflow_dispatch`, run on
`master`) repeatedly failed at the **pgbench standard (TPC-B) workload**:

```
pgbench: error: client 0 script 0 aborted in command 4 query 0:
ERROR:  current transaction is aborted, commands ignored until end of transaction block
pgbench: error: Run was aborted; the above results are incomplete.
```

(e.g. runs `27523520521` 2026-06-15, `27281470104` 2026-06-10.)

## Two separate facts, often conflated

1. **The underlying goopg bug is real and already fixed.** Spurious SQLSTATE
   40001 under READ COMMITTED (EvalPlanQual re-check cap of 3) plus an XID-leak
   hang on client disconnect. Diagnosed and fixed in commit `bdbe0fb0`
   (2026-06-08) — see [`pgbench-tpcb-hang-and-abort-fix-20260608.md`](pgbench-tpcb-hang-and-abort-fix-20260608.md).
   Confirmed on the current branch on 2026-06-16: `pgbench -c 8 -j 8 -T 30` ×2 →
   `0 failed, 0 aborts`, exit 0.

2. **The CI redness was a stale-`master` artifact.** The fix reached
   `origin/master` only via PR #36 (merge `baff747b`, 2026-06-15 23:06). Every
   red pgbench run above was on a *pre-merge* `master` commit. After fetch,
   `git branch -r --contains bdbe0fb0` includes `origin/master`, so current
   `master` already carries the fix.

## The durable problem — the loop is blind to pgbench

The fix was landed by a **dedicated manual session in an isolated worktree**, not
by the Ralph loop's own gate. The loop never would have caught it, because:

- `scripts/ralph-precommit-test.sh` **does** run the identical pgbench workloads
  (Part 2) and would reproduce this class of bug — **but `~/.ralph/ralph_loop.sh`
  never invokes it.** The gate was "advisory" in `AGENT.md`/`PROMPT.md` only.
- `PROMPT.md` actively told the agent to "run ESSENTIAL tests for the modified
  code only," so each loop ran targeted `go test` for its slice (e.g. pg_dump
  catalog work) and committed/pushed **without ever running pgbench**.

Net: a cross-cutting concurrency regression class had no automated tripwire in
the loop. It only surfaced in CI, on a ref the loop does not control.

## Fix landed this session (in-repo, committable)

1. **`.githooks/pre-commit`** (new) — runs the pgbench smoke on every commit
   (`RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh`) and rejects the
   commit on failure. `git commit --no-verify` is documented as forbidden.
2. **`scripts/ralph-precommit-test.sh`** — added `RALPH_PRECOMMIT_SCOPE`
   (`full` default / `smoke` = pgbench-only, ~2-3 min) so per-commit cost is
   bounded; and a **free-port probe** (`port_in_use` + scan from `RALPH_PRECOMMIT_PGPORT`
   upward) so a stray server on the fixed port can no longer make `pg_isready`
   connect to the wrong server and mis-report the gate.
3. **`Makefile`** — `make install-hooks` sets `core.hooksPath=.githooks`
   (idempotent; run once per clone).
4. **`.ralph/AGENT.md` / `.ralph/PROMPT.md`** — document the machine-enforced
   gate, mandate the pgbench smoke on every commit regardless of which files
   changed, and add a Branch/CI hygiene note (CI runs on `master`; a green branch
   gate doesn't make `master` green until merged; check merge status before
   re-diagnosing a reported `master` failure).

## Verification

- Smoke gate green: exit 0, all three workloads `0 failed`.
- Free-port fallback: with 5535 occupied, the gate logs
  `port 5535 busy; using free port 5536 instead` and still passes.
- `make install-hooks` idempotent; `core.hooksPath=.githooks`; hook executable +
  `bash -n` clean.
