# Enforcing the fire-set gate at commit level (M0145-0021a)

Status: **LANDED 2026-09-22** — the scope predicate, the gate stamp, and the
commit-msg enforcement are all in place. Movement: none (harness/policy only;
no production planner code touched).

Task: `.ralph/fix_plan.md` M0145-0021a. Kind: impl. Parent: M0145-0021.
Policy text: AGENT.md rule G9.

## Why enforcement, and why at commit level

M0145-0021 built the gate (`scripts/tpcds-fireset-gate.sh`: re-derive the fire
set per run by plan A/B, then execute every fire id at SF0.25 and SF1, fail on
any introduced timeout-class move) but left its invocation to discipline.
M0145-0018 is the standing lesson: an SF0.25-green firewall relaxation masked
a 60x+ SF1 regression (Q78: ~29 s → >1800 s timeout) precisely because nothing
*forced* the scale-sensitive check before the code landed. A gate that exists
but is not required decays — the next loop under pressure skips it.

The owner decision (progress-report review §3.5, delegated) was to enforce it
**mechanically at commit level**, not as a task-completion template. A
template lives in prose and can be skipped silently; a commit-msg check cannot
be skipped without `--no-verify`, which is already forbidden.

## The scope predicate — `scripts/fireset-scope.sh`

One script is the single source for "is this commit in scope", called by
`.githooks/commit-msg` on the staged file list, and callable by hand for a
pre-commit check. In scope:

- non-test `.go` under `internal/optimizer/` or `internal/planner/`;
- non-test `.go` anywhere under `internal/` or `cmd/` whose path matches
  `*cost*`, `*stat*`, or `*selfuncs*`.

Explicitly out:

- `internal/executor/` — checked BEFORE the keyword arm, because the executor
  carries ~10 `*stat*` filenames (`pgstat_*`, `extstats_*`,
  `sys_pg_statistic_ext*`) that would otherwise re-include it. The executor
  cannot move plan election; its own regression class (the executor can no
  longer run an elected shape) is covered by the SF0.25 sweep's `TIMEOUT`
  counter, which already gates every production commit. Including it would
  have made the expensive two-scale gate fire on executor-only work for no
  signal.
- `internal/testport/` — same exclusion commit-msg applies to `gated_go`.
- `*_test.go` and `*/testdata/*` — test content cannot move a plan either.

The `*stat*` keyword is deliberately broad (it also catches `state`,
`status`, `statement`, `sqlstate` filenames — e.g. `clog_statuscache.go`,
`statement_log.go`): it is the same substring convention commit-msg's
`arm_needed` already uses, and for a gate, over-inclusion is the safe
direction — the cost of a false positive is one gate run, the cost of a
false negative is an unmeasured election regression.

Default corpora: `tpcds-sf025 tpcds-sf1`. TPC-H stays opt-in via `CORPORA` —
the SF1 TPC-DS half is minutes per run, while a TPC-H fire execution is
heavier and the class it screens (scale-sensitive timeout/shape elections)
already has TPC-DS SF1 coverage.

## The stamp

`tpcds-fireset-gate.sh` now writes `tmp/gate-stamps/tpcds-fireset.json` on
every exit via an `EXIT` trap, using the shared `scripts/lib/gate-stamp.sh`
machinery (PASS / FAIL / SKIP-BLOCKED). The stamp hashes the staged code
(`code_tree`), so a stamp taken on a different index does not satisfy the
check — no self-attestation hole: the gate produces its own verdict, the hook
only verifies it. Exit 3 from the capture preflight (missing or HOLD source
datadir) maps to SKIP-BLOCKED, which `.githooks/commit-msg` accepts only
against an owner-maintained row in `.ralph/gate-exceptions.md` — the existing
mechanism, unchanged.

`tpcds-fireset` is also added to the exception table's `ROW_GATES` so a
SKIP-BLOCKED row for it validates like the other gates.

## What this deliberately does not do

- **Not a pre-commit (run-the-gate) hook.** The gate takes minutes; running it
  inside `.githooks/pre-commit` on every scoped commit would push every commit
  past the smoke gate's cost. The stamp requirement achieves the same binding
  at far lower friction: run the gate once, commit while the stamp is fresh.
- **No executor scope.** See above.
- **TPC-H not in the default CORPORA.** Available via `CORPORA` when a task's
  evidence calls for it.
