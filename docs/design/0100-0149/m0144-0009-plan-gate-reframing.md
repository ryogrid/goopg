# M0144-0009 — `plan-gate` reframing evaluation + golden-record rule

Status: recon landed 2026-09-20 (loop #41); decision recorded, M0137-0022
scope amended in place, runbook rule added. No production change.

Source: `METHODOLOGY4/03-forward-plan.md` §5 third + fourth bullets;
`.ralph/fix_plan.md` M0144-0009.

## What the gate measures today

`make plan-gate` (`Makefile:431`) → `plan-snapshot diff` against the newest
`plan_snapshots/*.txt` baseline, connecting to `PLAN_PORT=65433` — the live
TPC-H bench cluster. Both sides of the diff are fixed at pin/boot time:

| side | what it contains |
|---|---|
| baseline (`m0137-0005-rebaseline-20260915.txt`) | plans of the binary that served `:65433` at pin time |
| live (`:65433`) | plans of `bench/tpch/runtime_goopg/goopg-bin` — rebuilt `2b8afa538` during the 2026-09-20 owner 8-FK reload, sha256 `736aaa57…`, ≠ HEAD build `6a76d290…` (4 `internal/`/`cmd/` commits since) |

**The staged code appears in neither side.** As a pre-commit gate for
planner/executor changes it is structurally blind: it can only ever detect
(a) live-server binary rebuild drift and (b) data/stats drift on `:65433`
— an environment-health signal, not a code-regression signal. Its current
reading (14/22 diverged vs the 2026-09-15 pin) is the accumulated drift of
~100 landed commits plus the owner reload — exactly the "baseline
staleness" M0137-0022 describes. Re-pinning on a schedule refreshes the
pin but cannot fix the blindness: the next staged planner commit would
still be invisible to the diff.

## Options evaluated

| | keep re-pinning (M0137-0022 as filed) | reframe: diff a fresh private-clone HEAD build |
|---|---|---|
| what a FAIL means | live cluster drifted from its own pin | staged tree's plans differ from the pin |
| sees staged code | never | always |
| needs owner rebuild of `:65433` | yes, to pin HEAD plans | no — pin is taken on the private lane |
| per-run cost | ~5 s (22 EXPLAINs on live server) | ~1–2 min (pg_basebackup clone + crash-recovery boot + 22 EXPLAINs + diff) |
| R1 safety | reads shared `:65433` (allowed, but a shared-lane dependency) | shared cluster untouched except one read-only `pg_basebackup` (explicitly R1-permitted) |

The reframed path needs **no new mechanism**: every piece exists today —
`scripts/lib/tpch-private-clone.sh` (M0137-0007 + M0139 online fix:
`pg_basebackup -X fetch` against the running `:65433`, goopg implements
the `BASE_BACKUP` server side) gives a consistent private clone on a 55xx
port without stopping or waiting on the shared cluster;
`cmd/plan-snapshot` already takes `--host/--port/--db/--user/--password`
so it drives any lane; `scripts/goopg-test-run.sh` caps the lane. The same
pattern just proved out for TPC-DS SF1 in M0144-0008.

## Decision

**Reframe.** `plan-gate` should build the staged tree, clone `:65433`'s
data to a private lane via `tpch_private_clone_snapshot`, boot the staged
binary there, and `plan-snapshot diff` against the committed baseline. The
baseline itself is captured the same way (private lane, staged tree at pin
time), so pin == code and the diff is a true regression signal.

Re-pinning is **not** eliminated — it is re-scoped: a landed commit that
legitimately changes a plan still reds the gate until the pin moves with
it. The re-pin trigger becomes "each landed plan-moving change" (with the
existing attribution-census discipline from the M0137-0005 doc), not a
calendar schedule — same re-pin burden as today, but now the pin measures
code, not server drift.

## M0137-0022 — amended in place

Amended from "re-pin live-`:65433` plans (needs owner rebuild for exact
HEAD plans)" to: implement the private-lane capture/diff procedure above
as the `plan-gate`/`plan-snapshot-capture` path, then take the re-pin
through it (census the 14 recorded divergences, attribute to landed
mechanism classes, `tpch-spotcheck` PASS before pinning, capture on the
private lane, design doc). The owner-rebuild blocker is removed by
construction. See its task body in `.ralph/fix_plan.md`.

## Golden-record rule (pinned)

Added to `maintenance_prompts/cluster-ops-runbook.md` §"Record-level
golden dump": `bench/tpch/runtime_goopg/tpch-golden-20260919/` is the
record-level reference **for `:65433`'s current load** — if the cluster is
ever reloaded, the golden must be re-dumped (new dated dir + MANIFEST +
md5) before any record-level comparison is trusted. Was absent; now
present.

## D2 report fields

1. `CATEGORIES:`/`MATCH`: N/A — no parity capture this task (gate
   evaluation only).
2. shape-delta: N/A.
3. stats epoch: N/A — no capture taken.
4. seam-decline census: N/A — recon, no production change.
5. planning route: unchanged.
6. `Movement: none` — harness-hygiene decision; no plan movement claimed.
   `Parent: none`.
7. wall times: N/A.
