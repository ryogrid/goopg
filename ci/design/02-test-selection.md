# 02 — Test Selection

What the batch runs, what it skips, and how a skipped/failed test gets
promoted into the must-pass set. Grounded in
`analysis/tests-overview-260706/01-test-inventory.md` and
`05-duplicate-management.md`.

## A. The run set (per stage)

### Lane L step 1 — CI unit/component set (`stage-units.sh`)

Exactly the CI bar (same package set), with a nightly-sized timeout:

```bash
GOOPG_CG_UNIT=goopg-nightly-units GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G \
  scripts/goopg-test-run.sh env GOFLAGS=-p=4 \
  go test -timeout ${NIGHTLY_UNITS_TIMEOUT:-30m} $(go list ./... | grep -vE \
    'internal/testport|internal/server|internal/testutil/cluster|internal/testutil/replcluster|internal/testutil/pgcluster|internal/testutil/pubsubcluster|internal/testutil/tpch|/bench/')
# (the exact CI package set / EXCLUDE, run DIRECTLY: the precommit tool
#  hard-codes -timeout 10m, which internal/initdb was observed blowing under
#  nightly co-load — pgbench SF50 + the TPC-H data copy share the disk)
```

Note the wrapper env vars sit **before** `goopg-test-run.sh` — the wrapper
reads `GOOPG_CG_UNIT`/`GOOPG_MEM_*` from its *own* environment; placing them
after it (via `env`) would leave the wrapper on the default `goopg-test`
unit, the exact collision with the Ralph loop this design exists to avoid.

Every package failure here is a **regression** (exit≠0). There is no
expected-fail list for this stage — HEAD is expected green (it is the
per-commit bar the Ralph loop already enforces).

### Lane L step 2 — race detector (`stage-race.sh`)

`make race-gate RACE_TIMEOUT=${NIGHTLY_RACE_TIMEOUT:-45m}` (same EXCLUDE
list, `-race`; timeout raised from the 15m default for nightly co-load),
wrapped the same way with `GOOPG_CG_UNIT=goopg-nightly-race`. Also
no-expected-fail. Race findings are regressions by definition.

### Lane H step 1 — oracle-port suite (`stage-testport.sh`)

```bash
GOOPG_CG_UNIT=goopg-nightly-testport GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G \
  scripts/goopg-test-run.sh \
  go test -v -timeout 120m ./internal/testport/ \
  2>&1 | tee "$RUN_DIR/testport/go-test.log"
```

**No `-run` filter — deliberate.** 56 of the 60 must-pass rows name
`TestPort_*` functions, but 4 (`e2e-failover-pg-to-goopg-async/sync`,
`e2e-failover-goopg-to-pg-async/sync`) are pinned by
`TestE2E_FailoverPGtoGoopg` / `TestE2E_FailoverGoopgToPG`
(`internal/testport/e2e_failover_*_test.go`); a `TestPort_`-only filter would
silently skip them while reporting green. Running the whole package also
picks up the in-package SSI/durability e2e tests. This single invocation
covers:

- the **60 `port/yes` rows** of
  `docs/test-port/postgres-oracle-port-status.csv` — each row's `rationale`
  names the pinning Go function (56× `TestPort_*`, 4× `TestE2E_Failover*`);
- **`TestPort_RegressSuite`** — 232 upstream regress cases as subtests
  (`port→run`, `excluded→Skip`, `defer→Skip`, per the framework) — **but see
  the regress gating rule below: a mismatch surfaces as SKIP, not FAIL**;
- **`TestPort_IsolationSuite`** + **120 per-spec strict
  `TestPort_Isolation*` functions** (no per-spec function exists for the one
  known-fail spec, `deadlock-parallel`);
- the TAP-port families (initdb, pg_ctl, pg_dump, pg_amcheck, pg_waldump,
  recovery, subscription, pgbench, psql …).

**Regress gating rule (closes a real hole):** the regress framework maps an
output mismatch to status `defer` (`framework/regress.go`) and
`TestPort_RegressSuite` turns every non-`port` status into `t.Skip` — a
diverging regress case therefore **never fails the Go test**; it appears as
`--- SKIP ... deferred: output mismatch`. Nothing else in the repo consumes
`docs/test-port/regress-diff-baseline.csv` programmatically. So the batch's
result parser MUST join the suite's skip lines against the baseline CSV:
any case listed `status=pass` in `regress-diff-baseline.csv` (127 cases)
that reports an output-mismatch skip is classified a **regression**, exactly
as if it had failed. Isolation has no such hole — the 120 strict per-spec
functions fail hard on divergence.

**Wedge-recovery rule (added 2026-08-06): recovery must not re-bootstrap the
shared fixtures.** The gating rule above has a companion hazard, and until this
loop it was the single largest source of nightly action items. Regress cases
share one cluster and one set of `test_setup.sql` fixture tables. When a case
hits `regressCaseTimeout` (120 s) the suite marks the cluster poisoned and, at
the next case, restarts it — correct, and the only way to drop the orphaned
backend. What was *not* correct is what followed: recovery re-ran
`test_setup.sql` "to restore shared fixture tables". A restart preserves the
data directory, so the fixtures were never lost, and the re-run is neither
idempotent nor a no-op — psql runs without `ON_ERROR_STOP`, so every
`CREATE TABLE` fails with "relation already exists" while the `INSERT`/`COPY`
after it succeeds. **Every fixture table doubles** (measured: `int4_tbl` 5→10,
`text_tbl` 2→4, `varchar_tbl` 4→8, `onek` 1000→2000, `tenk1` 10000→20000).

Every later case that reads a doubled table then produces a *genuine* output
mismatch, which the gating rule above dutifully files as a regression. One
wedged case therefore manufactured an unbounded tail of unattributable
"regressions": run `20260806-191958` filed **9 items for 1 real event**
(`multirangetypes` wedged; `numerology`, `portals_p2`, `select`, `select_into`,
`text`, `truncate`, `union`, `varchar` were fixture casualties), and runs
`20260802-014405`/`20260803-013955`/`20260804-005028` show the same shape at
`aggregates`/`jsonb`/`misc`. That the wedge case *moves* between nights while
the casualty set tracks it is the signature: the casualties are downstream of
the recovery, not of any code change. Note this is a different failure mode
from the summarizer's truncated-output class (04 §C.1) — the casualty output is
complete and really does differ, so no report-side rule can filter it; the
harness must not corrupt the fixtures in the first place.

`restoreRegressFixtures` (`internal/testport/regress_suite_test.go`) is
therefore the only entry point recovery uses: it re-runs `test_setup.sql` **only
when the fixtures are genuinely absent** (`fixturesPresent()` probes `onek`), so
a restart onto a live data directory keeps them pristine and a restart onto an
empty database still bootstraps. Guard:
`TestPort_RegressSuiteRecoveryKeepsFixturesPristine` asserts the post-recovery
cardinalities and then performs the *unguarded* restore and asserts the
doubling, so it cannot pass vacuously (verified: removing the guard fails it on
all five tables).

This bounds the blast radius of a wedge to the wedged case itself; **what wedges
the cluster is a separate, still-open question** — `multirangetypes` completes
in 0.18 s standalone at HEAD, so the trigger is a whole-suite resource/state
condition, tracked as its own M-NIGHTLY item and deferral-ledger row.

**Dedup rule (from `05-duplicate-management.md`): the Go `testport` entry
point is the ONLY execution path.** The batch never additionally drives
`scripts/pg-regress-runner.sh` or per-spec CSV rows — those CSVs are tracking
metadata for the same cases, and double-running would double-count and
double-cost.

**Missing client binaries:** `clientToolBin()` resolves via `PATH` then
`postgres/local_install/bin` and returns empty when absent, upon which the
calling test `t.Skip`s. The batch
parses `--- SKIP` lines and reports a per-run skip count; a *new* skip for a
tool that was present the previous night is surfaced as `env-drift`
(informational, not a failure).

### Lane H step 2 — pgbench nightly run (`stage-pgbench.sh`)

Self-contained stage (server plumbing mirrored from
`scripts/ralph-precommit-test.sh`: free-port probe from 5555, throwaway
per-run data dir, capped server `goopg-nightly-pgbench` 6G/8G, pinned PG 18.3
client tools, SIGKILL teardown): build → initdb → `pgbench -i -s 50` →
standard / `-N` / `-S`, each **`-c 100 -j 20 -T 180`** (env-overridable via
`NIGHTLY_PGBENCH_SCALE/CLIENTS/THREADS/T`). Gate: **0 failed transactions**
on all three workloads. TPS is recorded but never gates (doc 04).

Every pgbench invocation runs under an outer `timeout -k 30 --signal=INT`
clamp (load 1800 s; workloads T+600 s): at c=100 a server bug can leave all
clients hung on never-returning queries, and pgbench then ignores its own
`-T` deadline indefinitely (observed live 2026-07-06 — 0.0 tps for 56 min).
A clamped workload is recorded FAILED and the remaining workloads still run.

Deliberately NOT delegated to `ralph-precommit-test.sh`: the nightly
parameters differ from the per-commit smoke (`-s 1 -c 2 -j 2 -T 30`), and
parameterizing the shared tool would risk changing the git-hook gate that
runs on every commit.

### S2 — TPC-H

Doc 05. Gates: spotcheck row counts (Q12=2, Q13 per
`bench/tpch/spotcheck_expected.env`), per-query success, row-count anchors.
Time is informational.

## B. Result classification (per test / per case)

Parsed from `go test -v` output (`--- PASS/FAIL/SKIP`) plus stage exit codes,
joined against three data sources **at run time** (never hard-coded):

1. `docs/test-port/postgres-oracle-port-status.csv` — via the same semantics
   as `internal/testport/framework/status.go` (`status` ∈ port/defer/excluded,
   `pass_required` ∈ yes/no).
2. `docs/test-port/regress-diff-baseline.csv` — the 127 `status=pass` rows
   gate the regress subtests via the mismatch-skip join described in §A.
   (Aside for readers: `docs/test-port/upstream-regress-coverage.md` is a
   stale 2026-06-09 render; the baseline CSV is the authority.)
3. `ci/batch/expected-failures.csv` — batch-local, per-case expected failures.
   Schema: `case_id,scope,reason,since,tracking`. Seeded with:

   ```csv
   case_id,scope,reason,since,tracking
   deadlock-parallel.spec,isolation,known-fail per upstream-isolation-coverage (120/121 pass),2026-07-06,docs/test-port/upstream-isolation-coverage.md
   ```

   Regress-case expectations are NOT duplicated here — the regress framework
   already skips `excluded`/`defer` cases itself, and
   `docs/test-port/regress-diff-baseline.csv` (127 pass / 1 excluded) remains
   the per-case diff baseline of record.

| Observation | Classification | Effect on exit code |
|-------------|----------------|---------------------|
| must-pass test FAILs | **regression** | batch fails (exit 2) |
| baseline-`pass` regress case reports an output-mismatch SKIP | **regression** (the §A join rule) | batch fails (exit 2) |
| must-pass test PASSes | pass | — |
| expected-fail case FAILs | known-fail (informational) | none |
| expected-fail case PASSes | **promotable** 🎉 notice in summary | none (human then promotes, see D) |
| test SKIPs (binary/data missing) | skip; `env-drift` if newly skipped | none |
| process dies by signal 9, no panic | **resource-kill** — check the stage scope's cgroup-OOM events (`dmesg`/`journalctl`; doc 03 §C) | batch marked `inconclusive` (exit 3), not regression |

Every observation classified **regression** (and `perf-drastic`/`fail(build)`)
also becomes an `AI-` item in `ci/logs/action-items.md`, which feeds the Ralph
loop's standing top-priority triage milestone — doc 07.

## C. What is deliberately NOT run nightly

- The 8 `defer` rows of port-status.csv (recovery/subscription remainders
  D-003/D-004, regress remainder D-001, connstr scripts D-005l, modules D-006,
  contrib D-007, WD-002, AC-003) — each keyed to a
  `deferred_to` milestone; their Go entry points either don't exist yet or
  Skip via the framework. They enter the batch automatically once promoted.
- `scripts/pg-regress-runner.sh --all` and `scripts/pg-oracle-diff.sh` —
  vanilla-PG comparison lanes; out of nightly scope (and redundant with the
  testport suite per the dedup rule).
- `make pgbench-compare*` heavy TPS matrix (`-c 100 -j 100 -T 180`) — a
  manual perf campaign, not a nightly regression check; the m0099 numbers
  stay a manually-refreshed baseline.
- HammerDB TPC-H **load** (`bench/tpch/cmd/hammerdb_load`) — the nightly
  reuses the existing loaded data dir; reloading nightly would invalidate the
  Q13 pin (doc 05) and add ~hours.
- `make pgo-profile`, pprof captures, `runtimeshim_go_matrix.sh` — tooling,
  not regression gates.

## D. Promotion workflow (deferred/failed → must-pass)

Unchanged from the established process; the batch is a pure consumer:

1. A previously failing case passes (often first noticed via the batch's
   **promotable** notice).
2. Human/agent flips the authority CSV(s): `status`→`port`/`pass`,
   `pass_required`→`yes`, `rationale` names the `TestPort_*` func, clear
   `deferred_to`. Both CSVs where applicable (port-status roll-up AND
   target-inventory per-spec — the two-CSV rule).
3. Regenerate rendered docs via the matching `cmd/gen-*` tool
   (`gen-oracle-port-status`, `gen-oracle-inventory`, `gen-regress-coverage`,
   `gen-isolation-coverage`, `gen-tap-coverage`).
4. If the case was in `ci/batch/expected-failures.csv`, delete its row.
5. Next nightly run picks the new must-pass set up automatically — **no batch
   code change**.

## E. Suite-level runtime expectations (for budget sanity, not gating)

| Stage | Expected wall clock |
|-------|---------------------|
| S0 preflight + build | ~2–4 min |
| Lane L units | ~10 min uncontended; up to `NIGHTLY_UNITS_TIMEOUT` (30m) under co-load |
| Lane L race | ~15 min uncontended; `RACE_TIMEOUT` raised to 45m for the nightly |
| Lane H testport (incl. regress+isolation) | not yet measured as one run; budget `-timeout 120m`, record actual on first nights |
| Lane H pgbench nightly (s=50, c=100, j=20, T=180×3) | ~12–15 min |
| S2 TPC-H | sweep ≤ 2 h hard + ≤ ~35 min setup/spotcheck/EXPLAIN overhead (doc 05 §D scope note); baseline full pass 1469 s |
| **Total** | ~2.5–5 h, dominated by testport + TPC-H |

First implementation nights should treat the testport duration as a
measurement task; the recorded per-stage durations in `summary.json` (doc 04)
become the batch's own trend data.
