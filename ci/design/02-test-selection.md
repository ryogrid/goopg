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

**Per-package sharding (added 2026-08-17, AI-20260815-011722-001 /
AI-20260816-005117-001).** A package can blow `RACE_TIMEOUT` without being
racy at all, when it is slow *and* internally sequential — the timeout is
per test binary, so a package with no `t.Parallel()` fan-out gets no benefit
from the stage's `GOFLAGS=-p=4`. `internal/initdb` is the known case: 122
call sites of the full on-disk `initdb.Init(...)` bootstrap
(`internal/initdb/initdb.go:1331`) across 38 files at ~27–29s each under
`-race`, only `relcache_init_test.go` calling `t.Parallel()` — ≈50–70 min
sequential, which timed out at 45m on two consecutive nightlies
(`panic: test timed out after 45m0s`, zero `DATA RACE` matches).

The policy answer is **re-partition, never de-scope**: the fix must not be
an `RACE_EXCLUDE` entry, a `testing.Short()` gate, or a raised global
timeout, because each of those trades away the coverage the gate exists for
(unlike the `RACE_EXCLUDE` entries above, which are justified by the
integration suite covering the same code). Instead `make race-gate` fans a
listed package out into `RACE_SHARDS` concurrent `go test -race -run <regex>`
invocations over a disjoint round-robin partition of its `go test -list`
test names:

- `RACE_SHARD_PKGS` (default `internal/initdb`) — import-path suffixes to shard.
- `RACE_SHARDS` (default 4) — shards per listed package.
- `RACE_SHARD_ONLY=1` — skip the bulk run, to time the shard set alone.

Every other package still runs through one bulk `go test -race` exactly as
before. Two self-checks keep the sharding honest, both failing the gate: the
per-shard test counts must sum to the `go test -list` count (catches
partition drift), and an empty test list for a listed package is a hard
error rather than a silently-skipped package (catches a build failure or a
stale suffix in `RACE_SHARD_PKGS` masquerading as success). Measured
2026-08-17: 4 shards, 152+151+151+151 = 605 tests, ≈19m56s wall-clock,
slowest shard 1154s — comfortably inside the 45m budget and faster than the
timeout it replaced.

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

- the **`port` rows (`pass_required=yes`)** of
  `docs/test-port/postgres-oracle-target-inventory.csv` — each row's
  `rationale` names the pinning Go function (`TestPort_*`/`TestE2E_Failover*`);
- **`TestPort_RegressSuite`** — 232 upstream regress cases as subtests
  (`port→run`, `excluded→Skip`, `defer→Skip`, per the framework) — a must-pass
  case (`status=pass`) that diverges now **FAILs** (see the regress gating rule);
- **`TestPort_IsolationSuite`** + **120 per-spec strict
  `TestPort_Isolation*` functions** (no per-spec function exists for the one
  known-fail spec, `deadlock-parallel`);
- the TAP-port families (initdb, pg_ctl, pg_dump, pg_amcheck, pg_waldump,
  recovery, subscription, pgbench, psql …).

**Regress gating rule:** the regress framework maps an output mismatch to status
`defer` (`framework/regress.go`); `TestPort_RegressSuite` turns a must-pass case
(`status=pass` in the consolidated inventory CSV) that reports `output mismatch`
into a **FAIL**, while `excluded`/`defer` cases and infra deferrals (timeout,
missing expected) stay `t.Skip`. The batch's result parser additionally joins
skip lines against the consolidated inventory CSV's regress-sql rows as
defense-in-depth: a `status=pass` case that still reports an output-mismatch
skip (an infra edge) is classified a **regression**. Isolation has no such
hole — the 120 strict per-spec functions fail hard on divergence.

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
the cluster is a separate, still-open question**, addressed by the wedge-probe
rule below.

#### Wedge-probe rule (added 2026-08-06)

A wedge is nondeterministic, rare, moves between cases, and happens on an
unattended host — so the harness must collect its own evidence at the moment it
happens, or the investigation never starts. Two facts from run
`20260806-191958` set the requirements:

- **It is not host overload and not a GC-thrashing server** (the two causes the
  action item names). That run's 232 cases sum to **298.5 s including the
  120 s wedge**, i.e. **178.5 s for the other 231** — faster than the 302 s the
  same suite took on the dev workstation with no wedge at all. The server was
  healthy immediately before and after; only one case stopped.
- **A single statement hangs past its own 5 s `statement_timeout`** (ExecuteSQL
  sets one on the session). The wait is therefore on a path that never observes
  the statement deadline — which is the defect to find, not a symptom of load.

Requirement: when a case crosses **`regressWedgeProbeAfter` (60 s, half the
per-case deadline)** the suite captures live state *while the backend is still
stuck*, and emits a bounded summary through `t.Log` — the nightly collects only
`testport/go-test.log`, so anything written elsewhere is invisible to triage.
`internal/testport/regress_wedge_probe_test.go` captures five things, chosen
because together they separate the surviving hypotheses:

| section | discriminates |
|---|---|
| `SELECT 1` liveness | one stuck backend vs. a dead/saturated postmaster |
| `pg_stat_activity` | *which* statement is stuck, and its wait event |
| `pg_locks` | a lock wait (and its holder) vs. a non-lock hang |
| goroutine dump (`debug=2`), filtered to goroutines blocked **>1 minute** | *where* in the server it is parked: lock, channel, mutex, or a spin that never checks `ctx` |
| server RSS from `/proc` | keeps or kills the GC-thrash hypothesis quantitatively |

Plus `psql-partial.out`: `ExecuteSQL` writes the killed client's partial output
to the bundle, because `framework.RegressResult` carries only a rationale string
and the tail of that output names the statement the server never answered.
Bundles land under `tmp/regress-wedge/<case>/` (override
`GOOPG_REGRESS_WEDGE_DIR`).

Two implementation constraints, both load-bearing:

- **The cluster gets its own pprof address** (`reserveLoopbackPort` +
  `GOOPG_PPROF_ADDR`). The server's built-in default is a fixed
  `127.0.0.1:6060`; when a bench cluster or peer test already holds it the bind
  fails at `Debug` level and the endpoint silently does not exist — precisely
  when the dump is needed.
- **Every probe query casts its columns to `text`.** goopg's
  `pg_stat_activity` emits an *empty* value for `pid` on internal sessions (3 of
  4 rows on an idle cluster), and an `int4` column carrying `""` fails a Go
  driver's parse for the whole query (`pq: strconv.ParseInt: parsing "":
  invalid syntax`). psql hides it by rendering a blank. The compat gap itself is
  a deferral-ledger row (2026-08-06).

Guard: `TestPort_RegressWedgeProbeNamesTheStuckStatement` runs the probe against
a cluster deliberately holding a long statement and asserts it names that
statement, reaches a dump containing real `internal/server` frames, and reads
the RSS; `TestRegressWedgeProbeStuckFilter` covers the >1-minute selection on a
synthetic dump, which a short live guard cannot reach. Non-vacuity is
demonstrated, not assumed: before the `::text` casts the guard failed on exactly
the `pg_stat_activity` assertion.

The probe collects evidence; it does not fix the wedge. The wedge trigger stays
an open M-NIGHTLY item with a deferral-ledger row, and its resume point is now
"read the probe sections from the first nightly that wedges".

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
joined against two data sources **at run time** (never hard-coded):

1. `docs/test-port/postgres-oracle-target-inventory.csv` — the consolidated
   authority (formerly port-status + inventory + regress baseline). Via the
   same semantics as `internal/testport/framework/status.go`: `status` ∈
   pass/failed/not-tried/excluded/port/defer, `pass_required` ∈ yes/no. The
   must-pass set is `pass_required == yes`; the regress join keys on
   `suite_id == "regress-sql"` (name = item_path basename minus `.sql`).
2. `ci/batch/expected-failures.csv` — batch-local, per-case expected failures.
   Schema: `case_id,scope,reason,since,tracking`. Seeded with:

   ```csv
   case_id,scope,reason,since,tracking
   deadlock-parallel.spec,isolation,known-fail per upstream-isolation-coverage (120/121 pass),2026-07-06,docs/test-port/upstream-isolation-coverage.md
   ```

   Regress-case expectations are NOT duplicated here — the regress framework
   already skips `excluded`/`defer` cases itself, and the consolidated
   inventory CSV's regress-sql rows are the per-case baseline of record.

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

- The `defer` rows of the consolidated inventory CSV (recovery/subscription
  remainders, modules/contrib, etc.) — each keyed to a `deferred_to`
  milestone; their Go entry points either don't exist yet or Skip via the
  framework. They enter the batch automatically once promoted.
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
2. Human/agent flips the authority CSV row (the one-CSV rule — see
   `docs/test-port/README.md`): `status`→`port`/`pass`, `pass_required`→`yes`,
   `rationale` names the `TestPort_*` func, clear `deferred_to`.
3. Regenerate rendered docs: `make regen-testport` (runs all `cmd/gen-*`).
4. If the case was in `ci/batch/expected-failures.csv`, delete its row.
5. Next nightly run picks the new must-pass set up automatically — **no batch
   code change**.

## E. Suite-level runtime expectations (for budget sanity, not gating)

| Stage | Expected wall clock |
|-------|---------------------|
| S0 preflight + build | ~2–4 min |
| Lane L units | ~10 min uncontended; up to `NIGHTLY_UNITS_TIMEOUT` (30m) under co-load |
| Lane L race | ~15 min uncontended; `RACE_TIMEOUT` raised to 45m for the nightly. `internal/initdb` is sharded ×4 (≈20 min for the shard set alone, measured 2026-08-17) — see §"Per-package sharding" |
| Lane H testport (incl. regress+isolation) | not yet measured as one run; budget `-timeout 120m`, record actual on first nights |
| Lane H pgbench nightly (s=50, c=100, j=20, T=180×3) | ~12–15 min |
| S2 TPC-H | sweep ≤ 2 h hard + ≤ ~35 min setup/spotcheck/EXPLAIN overhead (doc 05 §D scope note); baseline full pass 1469 s |
| **Total** | ~2.5–5 h, dominated by testport + TPC-H |

First implementation nights should treat the testport duration as a
measurement task; the recorded per-stage durations in `summary.json` (doc 04)
become the batch's own trend data.
