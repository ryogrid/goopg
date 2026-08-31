# 0126-0012 — acceptance measurement and the conditional default flip

> **STATUS 2026-08-03 — EXECUTED; OUTCOME: DOCUMENTED NO-GO (run 1), CONFIRMED
> BY -0013's RE-CHECK (run 2).** Run 1
> (`analysis/cost-driven-second-try-200731/evidence/acceptance-run-1.txt`,
> HEAD e85e5347) failed clauses 1–3 on Q9 alone → triggered -0013. Run 2
> (`evidence/acceptance-run-2.txt`, HEAD e13d6c6f) is strictly worse (Q5 also
> hang-class) and is the milestone's FINAL no-go. No flip;
> `GOOPG_COST_DRIVEN_JOINORDER` remains default OFF.

| field | value |
| --- | --- |
| status | superseded — originally executed no-go (2026-08-03) |
| superseded by | [leftdeep-joins/](../leftdeep-joins/) — MHJ retired (M0127) |
| date | 2026-07-31 |
| task | M0126-0012 — terminal task; **flip-or-documented-no-go, by construction** |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` — §"The acceptance bar" is normative and is NOT restated here |
| design of record | user directive 2026-07-31; bundle **09** Stage 3 protocol (reused as the measurement protocol). Supersedes bundle **07** §6's "no default change" — that sentence scoped the *bundle*, and the milestone doc records the supersession |
| depends on | `0126-0011` (MUST precede — single-variable A/B; see milestone doc), `0126-0008` (protocol), `0126-0010` if entered |

## 1. Scope

Measure all four clauses of the milestone's acceptance bar at final HEAD
(protocol identical to -0008's, which runs BOTH arms — integer planner and
cost-driven — yielding `query | R0 s | integer s | cost-driven s`; clauses 2/3
are judged against the FASTER of R0 and the final-HEAD integer arm, per the
milestone's bar definition: a stale R0 alone could accept a flip that regresses
against the contemporaneous integer planner). Symmetric timeouts, quiet host,
matched fresh servers, cgroup cap. Recorded to
`analysis/cost-driven-second-try-200731/evidence/acceptance-run-1.txt` as a
**clause-by-clause verdict**. Then exactly one of:

- **Bar met → flip.** Make cost-driven join order the default
  (`internal/planner/bushy.go:13-21` — the env-var read becomes default-on with
  the variable retained as an opt-*out*), re-capture the plan snapshot, and
  update every repo statement that says cost-driven "ships off by default"
  (grep for `GOOPG_COST_DRIVEN_JOINORDER` across `docs/`, `analysis/`,
  `internal/planner/` comments; enumerate the touched statements in the commit
  message). Note that the env var's `mhjPackingEnabled=false` side-effect
  assignment is now redundant after -0011 — **note it, do not delete it** (it is
  load-bearing if KS3 is ever reverted).
- **Bar missed → documented no-go, and M0126-0013 is TRIGGERED.** The no-go
  document names the failing clause(s), the residual queries with their -0009
  attributions, and hands the class-(c) evidence files to -0013.

**Both outcomes are successful completions. An unmeasured outcome is the only
failure.**

## 2. Files and symbols touched (flip path only)

| file | symbol | change |
|---|---|---|
| `internal/planner/bushy.go:13-21` | `init()` / `costDrivenJoinOrder` | default-on; env var becomes opt-out |
| `plan_snapshots/` | — | re-capture (`LABEL=m0126-costdriven-default`) |
| repo-wide "ships off by default" statements | — | enumerated update |

## 3. Gates

Full timed 22-query TPC-H SF1 acceptance run; DS05 (zero row + checksum deltas,
recorded as 57/99+42/99); SPOT; PLAN re-snapshot with hand review (the flip
changes plans by design — same discipline as -0011 §2); UNITS; SMOKE.

## 4. Stop / decision conditions

The bar clauses themselves. No partial flip: if any clause fails, the flip does
not land — there is no "flip with a known regression" outcome.

## 5. Rollback

Revert the flip commit (the env-var machinery is untouched, so behaviour
reverts exactly); restore the snapshot. Preserve the acceptance run either way.

## 6. What this doc deliberately does not decide

The remediation's design (-0013) and the final verdict after remediation
(-0013 re-runs this task's §1 measurement as its last step).
