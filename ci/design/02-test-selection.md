# 02 — Test Selection

What the batch runs, what it skips, and how a skipped/failed test gets
promoted into the must-pass set. Grounded in
`analysis/tests-overview-260706/01-test-inventory.md` and
`05-duplicate-management.md`.

## A. The run set (per stage)

### Lane L step 1 — CI unit/component set (`stage-units.sh`)

Exactly the CI bar, via the established tool:

```bash
GOOPG_CG_UNIT=goopg-nightly-units GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G \
  scripts/goopg-test-run.sh \
  env RALPH_PRECOMMIT_SCOPE=units GOFLAGS=-p=4 scripts/ralph-precommit-test.sh
# inner suite == go test -timeout 10m $(go list ./... | grep -vE \
#    'internal/testport|internal/server|internal/testutil/cluster|internal/testutil/replcluster|internal/testutil/pgcluster|internal/testutil/pubsubcluster|internal/testutil/tpch|/bench/')
# (functionally identical to the CI workflow's multi-line EXCLUDE string)
```

Note the wrapper env vars sit **before** `goopg-test-run.sh` — the wrapper
reads `GOOPG_CG_UNIT`/`GOOPG_MEM_*` from its *own* environment; placing them
after it (via `env`) would leave the wrapper on the default `goopg-test`
unit, the exact collision with the Ralph loop this design exists to avoid.

Every package failure here is a **regression** (exit≠0). There is no
expected-fail list for this stage — HEAD is expected green (it is the
per-commit bar the Ralph loop already enforces).

### Lane L step 2 — race detector (`stage-race.sh`)

`make race-gate` (same EXCLUDE list, `-race -timeout 15m`), wrapped the same
way with `GOOPG_CG_UNIT=goopg-nightly-race`. Also no-expected-fail. Race
findings are regressions by definition.

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

### Lane H step 2 — pgbench smoke (`stage-pgbench.sh`)

`RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` — build →
initdb → pgbench standard / `-N` / `-S`, each `-T 30 -c 2 -j 2`. Gate:
**0 failed transactions** on all three workloads. TPS is recorded but never
gates (doc 04).

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
| Lane L units | ~10 min (CI `-timeout 10m`) |
| Lane L race | ~15 min (`RACE_TIMEOUT=15m`) |
| Lane H testport (incl. regress+isolation) | not yet measured as one run; budget `-timeout 120m`, record actual on first nights |
| Lane H pgbench smoke | ~2–3 min |
| S2 TPC-H | sweep ≤ 2 h hard + ≤ ~35 min setup/spotcheck/EXPLAIN overhead (doc 05 §D scope note); baseline full pass 1469 s |
| **Total** | ~2.5–5 h, dominated by testport + TPC-H |

First implementation nights should treat the testport duration as a
measurement task; the recorded per-stage durations in `summary.json` (doc 04)
become the batch's own trend data.
