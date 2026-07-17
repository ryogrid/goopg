# Test-Gate Speed-ups — Design

Design date 2026-07-17. **This directory contains the DESIGN only** — no gate
script, test harness, `.ralph/` prompt file, or production code is modified by
this bundle. Every change appears as a fenced code/diff block inside the
numbered documents, to be applied by later, individually-gated implementation
loops (staging order in [06](06-prompt-changes-and-rollout.md)).

## Purpose

Test/verification gates pace goopg development: every Ralph loop and every
interactive change pays the pre-commit unit suite, the per-commit pgbench
smoke hook, and (per practice cards) TPC-H / race / regress gates, while the
nightly batch re-runs the whole surface. This bundle inventories where the
wall-clock actually goes ([01](01-where-time-goes.md)) and proposes speed-ups
that shorten gates **without materially weakening their quality signal** —
each proposal carries an explicit quality-risk statement, a mitigation, and an
upstream-PostgreSQL precedent where one exists. The two headline levers —
skipping durability fsyncs in throwaway test clusters and placing test data
directories on tmpfs — are exactly what upstream PostgreSQL's own test harness
has always done (`pg_regress.c:2340` runs `initdb --no-clean --no-sync`;
`PostgreSQL::Test::Cluster::init` writes `fsync = off` into every test
instance's config, `Cluster.pm:685`), so adopting them is *increasing* harness
parity with the oracle, not cutting corners.

## Document map

| Doc | Contents |
|-----|----------|
| [01-where-time-goes.md](01-where-time-goes.md) | Measured baseline: per-gate cold/warm durations from `ci/logs/history.jsonl`, decomposition (Go test cache, initdb fsync-per-bootstrap, testport boot-per-test, `t.Parallel` census), ranked opportunity list |
| [02-durability-off-for-test-servers.md](02-durability-off-for-test-servers.md) | `goopg init --no-sync` adoption in gates/harnesses (zero prod code); the proposed PG-compatible `fsync` GUC; the durability-must-stay-on allowlist; probe evidence |
| [03-tmpfs-data-dirs.md](03-tmpfs-data-dirs.md) | tmpfs/`/dev/shm` data dirs: feasibility (O_DIRECT removed), cgroup-v2 memory-accounting interaction and sizing, per-gate opt-in/opt-out table, fallback design, probe evidence |
| [04-parallelism-and-bootstrap-caching.md](04-parallelism-and-bootstrap-caching.md) | initdb template caching (PG 16 `INITDB_TEMPLATE` model) and per-package `t.Parallel()` pilots with hazard checklist |
| [05-gate-scoping-and-cache-policy.md](05-gate-scoping-and-cache-policy.md) | Normative Go-test-cache-warmth rules; affected-package selection (proposed, ranked last on quality risk); smoke-gate policy |
| [06-prompt-changes-and-rollout.md](06-prompt-changes-and-rollout.md) | Proposed wording changes to `.ralph/AGENT.md` / `.ralph/PROMPT.md` / practice cards (apply manually — `.ralph/` is protected), staged rollout order, and the A/B parity acceptance protocol |

## Ranked proposal summary

Expected wins are grounded in the measurements of doc 01 and the probes of
docs 02/03 (probe: `goopg init` 0.77 s → 0.14 s with `--no-sync` → 0.09 s on
tmpfs; the `^TestInit` initdb subset 17.9 s on disk → 1.6 s on tmpfs).

| # | Proposal | Doc | Expected win | Quality risk | Prod code touched? | Rollout stage |
|---|----------|-----|--------------|--------------|--------------------|---------------|
| 1 | `--no-sync` init in gates + harnesses | 02A | ~0.6 s × every test-cluster init: ≈100 s off `internal/initdb`, ≈3–5 min off a full testport run (~500 boot sites), seconds off every commit's smoke hook | Low — init durability is never what these tests assert; allowlisted recovery tests stay durable | test-harness only (`SyncInit`, `DurableTempDir`) | 1 |
| 2 | tmpfs data dirs (opt-in per gate; race gate excluded) | 03 | 11× on the measured initdb subset; large fractions of initdb/testport/server package time; faster smoke init | Low–medium — cgroup memory accounting + mem_guard blindness to shmem; sizing table, headroom preflight, disk fallback, PID-liveness sweep required | none | 2 |
| 3 | initdb template caching in `testutil/cluster` + initdb test helpers | 04 §1 | ~660 bootstraps (165 initdb-test + ~500 testport boot sites) → a handful per process; O(minutes) cold-cache | Low — per-process template keyed by init args, sysid re-randomized per clone; no cross-run staleness | test-harness only | 3 |
| 4 | `t.Parallel()` per-package pilots (initdb → executor → catalog) | 04 §2 | 2–3× on I/O-wait-heavy packages | Medium — hidden shared state, memory amplification under cgroup caps, port-allocator TOCTOU in testport | tests only | 4 |
| 5 | PG-compatible `fsync` GUC (default `on`), off in throwaway test servers | 02B | write-heavy suite phases, `pgbench -i`; modest for fixed `-T` phases | Medium — can mask fsync-ordering bugs; nightly keeps a durability-on lane | yes (WAL/smgr sync paths) | 5 |
| 6 | Cache-warmth policy (never `-count=1` in gates, cache-key hygiene) | 05 §1 | protects the existing dominant 7–8× lever | None | none | immediate (policy) |
| 7 | Affected-package test selection | 05 §2 | typical-commit `units` 310 s → <60 s (cold-cache days: much more) | **Highest** — cross-package escapes (test-only imports, build tags, on-disk-format coupling); requires a sustained-green nightly baseline first | none | 6 (last, optional, preconditioned) |

## Non-goals

- Shortening the pgbench smoke's three fixed `-T 30` measurement windows —
  rejected in [05 §3](05-gate-scoping-and-cache-policy.md); the windows are the
  perf-tripwire signal itself.
- Any change to what the nightly batch runs; it remains the full-surface
  authority and the backstop for every proposal here.
- Weakening crash-recovery / WAL-replay / restart-durability coverage: those
  test families are explicitly allowlisted to keep durability on
  ([02 §4](02-durability-off-for-test-servers.md)).

## Implementation status

2026-07-17 (follow-up session, same day): the mechanisms of stages 1, 3, and
5 are LANDED, effective for both the Ralph loop and interactive sessions —
02A (`--no-sync` in the smoke-gate init; `SyncInit`/`SyncRuntime` in
`testutil/cluster`; §4 allowlist applied: replcluster + pubsubcluster peers
and the durability/recovery/basebackup testport families opt out via
explicit options or `newDurableCluster`; `internal/initdb` NoSync sweep of
57 call sites with recovery/restart/crash/sync/checkpoint test files
excluded), 02B (real `fsync` GUC, default `on`, gating the WAL commit-flush
barrier, checkpoint smgr `Sync`/`SyncAll`, and the CLOG/pg_subtrans SLRU
syncs; `testutil/cluster` appends `fsync = off` unless `SyncRuntime`), 04 §1
(per-process init-template cache with mandatory sysid re-randomization and
the §1.4 structural guard `TestTemplateCloneEquivalence`), 05 §1 + 06 Part A
wording (AGENT.md, PROMPT.md, practice cards, plus a new repo `CLAUDE.md`
covering interactive sessions). Deliberate deviations from the letter of the
docs: the pgbench smoke server KEEPS `fsync = on` (that gate exists to catch
TPC-B group-commit concurrency regressions and its TPS windows are the
tripwire baseline — 02 §4's timing-coverage-shift concern applies to it
directly); WAL segment-lifecycle syncs (preallocation, dir fsyncs) stay
ungated (once per 16 MiB, off the commit path); initdb-package tests got the
NoSync sweep but not template cloning. Stages 2 (tmpfs), 4 (`t.Parallel`),
and 6 (affected-package selection) remain unimplemented proposals.

First divergence adjudicated per the 06 B.2 protocol, same day:
`TestPort_ProfileUpdate` hung the full suite (its workers swallowed query
errors and never reported counts — latent test bug, fixed to report), and
the underlying error was a fast-only reproducible `40001 could not
serialize access due to concurrent update (deadlock)` from plain autocommit
UPDATEs (fast 2/2 fail at ~2.3k TPS, durable 3/3 pass at ~1.3k TPS).
Attribution: NOT introduced by fsync=off — it is the known deferred
tuple-lock-FIFO divergence (ledger 0021-0012) surfacing at higher
throughput, exactly the timing-coverage *increase* doc 02 predicted.
Resolution: test durable-pinned with a dated comment; un-pin when
0021-0012 lands.

## Review round

2026-07-17: adversarial 3-lens agent review (R1 claims-correctness, R2
feasibility-ops, R3 quality-risk). 7 findings rated blocking across R2/R3,
all incorporated: the affected-packages selector was rewritten fail-open
(empty-selection and test-blind-closure holes closed, 05 §2.1); the
durability allowlist gained a concrete mechanism (`DurableTempDir`, 03 §3.4)
and `make race-gate` was excluded from tmpfs (fsync-latency schedule
coverage, 03 §2); the A/B protocol was rebuilt around mandatory paired
same-SHA runs with divergence adjudication (06 B.2); the smoke tmpfs
preflight's `set -e` abort bug and 512 MB→2 GB pg_wal sizing were fixed
(03 §3.2); the appendix timing reporter was made actually runnable (01);
template caching gained sysid re-randomization + a structural equivalence
guard (04 §1). Non-blocking findings (mem_guard shmem blindness, freePort
TOCTOU, soak-math on flake detection, cache-key limits incl. the
pg_waldump exec, prompt-wording over-application hazards) are folded into
the respective docs.
