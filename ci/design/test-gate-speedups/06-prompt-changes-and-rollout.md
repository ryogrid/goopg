# 06 — Prompt Changes and Rollout Protocol

Status: draft. Part of [test-gate-speedups](README.md).

Two halves: the wording changes that teach the agent loop the new knobs
(Part A), and the staged rollout with its acceptance protocol (Part B).

**Part A is proposals only.** `.ralph/` is protected loop infrastructure
("Protected Files" in `.ralph/PROMPT.md`); these diffs are to be applied
manually by the operator, or by a loop explicitly authorized to touch
`.ralph/`, at the rollout stage where the corresponding mechanism actually
lands. Applying a prompt change before its mechanism exists would make the
loop invoke knobs that don't work.

## Part A — proposed prompt/instruction wording

### A.1 `.ralph/AGENT.md` — "Pre-commit test gate" section (apply at stage 2)

Insert after the `RALPH_PRECOMMIT_SCOPE=units` command block (around line
191). NOTE for the applier: the command sits inside a ```` ```bash ````
fence — the closing fence line is included as context below so a mechanical
apply cannot land the bullets *inside* the code block:

```diff
 RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh   # unit/component suite only
 ```
+
+Fast-gate knobs (see ci/design/test-gate-speedups/):
+
+- The gate always inits throwaway clusters with `--no-sync` (since stage 1 —
+  this is unconditional, not gated by any flag; durability-allowlisted tests
+  use the durable helper instead, ci/design/test-gate-speedups/02 §4).
+- `RALPH_PRECOMMIT_TMPFS=1` — additionally places gate data dirs and
+  `t.TempDir()` roots on tmpfs (/dev/shm); falls back to disk automatically
+  when /dev/shm lacks headroom. Set it for gate runs on this host (a
+  consistent value keeps the test-cache key stable) — EXCEPT while working
+  on recovery/durability code before the disk-backed carve-out
+  (ci/design/test-gate-speedups/03 §3.4) covers the package you are testing.
+- NEVER pass `-count=1` to a gate's `go test` — it defeats the test-result
+  cache and turns a ~5 min warm `units` run into a ~40 min cold one.
+  `-count=1` is only for one-off validation runs (flake screens, probes).
+  A cached PASS is a real PASS (scope and limits:
+  ci/design/test-gate-speedups/05 §1 rule 2).
```

Stale-duration cleanup: the phrase "the ~10 min unit suite" lives in
`scripts/ralph-precommit-test.sh:46`'s comment, **not** in AGENT.md — update
it to "~5 min warm-cache / up to ~40 min cold" as part of stage 2's *script*
diff (script comments are not `.ralph/`-protected). AGENT.md itself carries
no unit-suite duration figure to fix.

### A.2 `.ralph/PROMPT.md` — gate rules (split across two stages)

In the "Headless Execution Reality" / gate-discipline area, append — the two
bullets land at DIFFERENT stages, because a prompt must never describe a knob
before its mechanism exists (the loop would set an env var the script
ignores and misreport speedups):

**Stage 0** (cache policy is pure text, no mechanism needed):

```diff
 - Run test/verification gates (`ralph-precommit-test.sh`, `tpch-spotcheck.sh`,
   `make race-gate`, pgbench) in the FOREGROUND. ...
+- Never pass `-count=1` to gate `go test` invocations (cache policy:
+  ci/design/test-gate-speedups/05). If a gate is unexpectedly slow, check
+  whether the test cache went cold (branch switch / toolchain change) before
+  suspecting a regression, and say which case it was in `Gates run:`.
```

**Stage 2** (once `RALPH_PRECOMMIT_TMPFS` exists in the script):

```diff
+- Set `RALPH_PRECOMMIT_TMPFS=1` for gate runs — EXCEPT when the change
+  touches recovery/durability paths and the disk-backed carve-out does not
+  yet cover the affected package (allowlist:
+  ci/design/test-gate-speedups/02 §4).
```

### A.3 Practice cards (`analysis/ralph-loop-kaizen/practice-cards/`)

Per-card one-liners, applied at the stage that lands the mechanism:

- **`wal-replication-change.md`** (stage 1 — this is the safety-critical
  one): add under "Known traps":

  ```markdown
  - **Durability allowlist:** crash-recovery / WAL-replay / restart-durability
    tests must NOT use `--no-sync` init, `fsync=off`, or tmpfs data dirs —
    they assert the durable path itself. New recovery tests default to the
    durable configuration (`SyncInit: true` once testutil/cluster grows it).
    List: ci/design/test-gate-speedups/02 §4.
  ```

- **`executor-planner-change.md`**, **`codec-storage-change.md`**,
  **`catalog-ddl.md`**, **`tpch-perf.md`** (stage 2): one shared line in the
  gate section. The exception clause is load-bearing: durability work is not
  always routed to the wal-replication card (crash-durability fixes have
  historically lived in codec/storage/TOAST territory), so every card
  carries it:

  ```markdown
  Run gates with `RALPH_PRECOMMIT_TMPFS=1` — except when the change touches
  recovery/durability paths (allowlist: ci/design/test-gate-speedups/02 §4).
  Never `-count=1` in a gate run.
  ```

- **`server-test.md`** (stage 3): note that `testutil/cluster` inits from a
  cached template by default and how to opt out (`SyncInit`) for durability
  tests.

### A.4 `.ralphrc` / hooks

No changes needed: the raised Bash timeouts already accommodate the *slow*
case, and nothing here changes hook wiring. Recorded explicitly so the
implementing loops don't invent one.

## Part B — staged rollout and acceptance protocol

### B.1 Stages (each = one Ralph loop or interactive session, own gates, own commit)

| stage | change | docs | acceptance before advancing |
|-------|--------|------|------------------------------|
| 0 (immediate, no code) | adopt the cache-policy rules as prompt text only (A.1's `-count=1` bullet; A.2's stage-0 bullet ONLY — the tmpfs bullet waits for its mechanism) | 05 §1 | none — pure policy, reversible by reverting wording |
| 1 | `--no-sync` in smoke gate + `SyncInit` in testutil/cluster + the `DurableTempDir` helper (03 §3.4) + wal-replication card warning (A.3 first bullet); resolve the durability allowlist to concrete test names and convert those tests to the durable helper | 02 A, 02 §4, 03 §3.4 | full testport suite green; allowlisted tests confirmed still running durable (grep the resolved list against the diff) |
| 2 | `RALPH_PRECOMMIT_TMPFS=1` support in `ralph-precommit-test.sh` (both seams; fixed `TMPDIR` path per 05 §1 rule 3; `TMPDIR` seam lights up per-package only where 03 §3.4 conversion is done); prompt updates A.1 + A.2 stage-2 bullet + stage-2 card lines; the script-comment duration fix (A.1 note) | 03 | smoke gate green with flag on and off; `df` fallback path exercised once (raise the threshold temporarily to force it); confirm a warm `units` run stays warm with the flag on (cache-key check — re-run whenever the TMPDIR path or sweep changes); record end-of-smoke `du -s "$DATADIR"` once to validate the ~1 GB pg_wal estimate |
| 3 | initdb template caching (incl. sysid re-randomization + structural equivalence guard, 04 §1.2/§1.4); initdb-package `NoSync` helper sweep; server-test card note | 04 §1, 02 A.3 | equivalence test green including the sysid-differs assertion; `internal/initdb` and testport wall-clock before/after recorded |
| 4 | `t.Parallel()` pilots, one package per loop: initdb → executor → catalog (→ testport, after bind-retry lands and memory is sized) | 04 §2 | per-pilot: 10× `-count=1` green in-loop + soak to ≥60 executions + race gate green + next 3 nightlies watched + timing recorded |
| 5 | `fsync` GUC feature loop (own design doc, sample-conf sync, race gate, smoke) then harness adoption behind the same allowlist | 02 B | normal feature-gate bar + A/B protocol below before any harness default flips |
| 6 (optional) | affected-package selection, only if 1–4 prove insufficient AND the nightly has a sustained-green (or fully-per-test-triaged) baseline (05 §2.2 precondition) | 05 §2 | A/B protocol; smoke hook remains unconditional forever |

Stages 1–3 are independent of each other in mechanism but ordered by
blast-radius (fsync-skip is the best-understood; tmpfs adds the cgroup
dimension; the template cache changes what "init" means in tests).

### B.2 A/B parity protocol (the "quality not weakened" proof)

Before any fast mode becomes a *default* (as opposed to an opt-in flag).
Designed against the actual baseline, which makes naive versions of this
protocol undecidable: the nightly has been `status=fail` **every night**
recently with ambient `regressions` spanning 1–96 (`ci/logs/history.jsonl`),
so "compare failure counts" cannot distinguish anything, and comparing runs
of *different SHAs* attributes nothing on a repo landing multiple
commits/day.

1. **Paired, same-SHA runs are mandatory** — alternating nights on moving
   HEAD are not acceptable. Concretely: after the normal (slow-lane) nightly
   completes, run a fast-lane batch **from a worktree pinned to the slow
   lane's recorded SHA**, in the off-hours window. This needs new (small)
   plumbing the batch does not have today — the run lock deliberately
   serializes executions and the stage scripts hardcode scope names, the
   TPC-H snapshot dir, and port 65434 — so the fast lane must parameterize
   `GOOPG_CG_UNIT` names, ports, and data-dir roots, and run sequentially
   after the slow lane (sequential is fine; concurrency is not the point,
   SHA-pairing is). This plumbing is part of the stage-5 work item.
2. **Adjudicate divergences, don't count failures.** Per paired run, diff the
   two lanes' `regressions[]` lists from `ci/logs/<ts>/summary.json` — note
   this is what the summary actually contains: per-stage status plus a
   run-level list of divergences vs the baseline CSVs, *not* per-test
   pass/fail sets — keyed by regression subject. For every subject present
   in one lane and not the other, re-run that test ≥5× in BOTH modes at the
   same SHA:
   - reproducible fast-only failure → **automatic stage rollback** plus a
     bug report (a masked product bug or a harness bug — both blocking);
   - bilateral / non-reproducible → ambient flake: record it in a
     pre-registered known-flaky list (seeded from the trailing two weeks of
     nightlies) and exclude it from the parity criterion, tracked separately.
3. **Acceptance:** ≥10 paired runs with **zero unadjudicated divergences and
   zero reproducible fast-only failures**, and the wall-clock improvement
   actually realized (produced by the timing reporter drafted in
   [01 Appendix](01-where-time-goes.md)). Stated honestly: 10 paired runs
   detect a fast-only failure mode that manifests 5% per run with probability
   ~40% — the protocol is a screen, not a proof; the durable guarantees are
   (a) the fast path stays opt-in until this screen passes, (b) rollback is
   automatic on the first reproducible divergence ever observed, including
   after acceptance, and (c) the durability allowlist never runs fast at all.
4. Record the verdict as a dated note in this bundle's README and only then
   change defaults (and prompts, per Part A's apply-at-stage rule).

### B.3 Measurement discipline

Every stage's commit message records before/after wall-clock for the gate it
touches, measured with the commands already printed in docs 01–03 (so numbers
stay reproducible). The rollout is complete when the steady-state
per-change cost is approximately: `units` warm ≈ 5 min, smoke ≈ 100–120 s,
testport full ≈ materially under its current ~1300 s warm floor — with the
nightly full batch unchanged as the authority.
