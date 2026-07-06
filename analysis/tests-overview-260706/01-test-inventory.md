# 01 — Test Inventory (what to run, and what is promotable)

Snapshot 2026-07-06. Re-read the cited files before a live run — counts drift.

---

## A. Go unit / component tests (`internal/<pkg>/*_test.go`)

Run by `go test`. There are **no `//go:build integration` tags** in the tree;
the heavy/light split is drawn by (a) the CI/`race-gate` EXCLUDE list and (b)
in-test `t.Skip(...)` / `testing.Short()` gates for missing client binaries.

### Package `_test.go` file counts (largest first)

| Package | # test files | Weight |
|---------|-------------:|--------|
| `internal/executor` | 243 | light unit (largest) |
| `internal/initdb` | 176 | mostly unit |
| `internal/wal` | 91 | unit |
| `internal/parser` | 74 | unit |
| `internal/testport` | 72 | **HEAVY** (boots clusters; needs psql/pgbench/pg_*) |
| `internal/server` | 65 | **HEAVY** (boots server; race-excluded) |
| `internal/planner` | 50 | unit |
| `internal/catalog` | 38 | unit |
| `internal/mvcc` | 26 | unit (SSI) |
| `internal/storage` | 24 | unit |
| `internal/access/btree` | 16 | unit |
| `internal/amcheck` | 13 | unit |
| `internal/config` | 10 | unit |
| `internal/testutil/{cluster,pgcluster,pubsubcluster,replcluster,tpch,util}` | 1–7 each | **HEAVY** harness helpers (race-excluded) |

Other packages with unit tests: access/btree, activity, aio, amcheck, analyzer,
auth, autovacuum, control, lockmgr, mctx, multixact, plpgsql, protocol,
runtimeshim, sqlkeywords, stats, vacuum.

### The "must-pass now" Go set = the CI unit step

CI (`.github/workflows/test.yml`, "Run unit and component tests") runs:

```bash
EXCLUDE='internal/testport|internal/server|internal/testutil/cluster|internal/testutil/replcluster|internal/testutil/pgcluster|internal/testutil/pubsubcluster|internal/testutil/tpch|/bench/'
go test -timeout 10m $(go list ./... | grep -vE "$EXCLUDE")
```

The excluded packages all boot a live server/cluster and are run by other means
(testport suites, TPC-H harness). This exact command is mirrored by
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` and by
`make race-gate` (same EXCLUDE, with `-race`).

---

## B. PostgreSQL oracle ports (`internal/testport/`)

**248 `TestPort_*` functions across 72 files.** Ordinary Go tests
(`func TestPort_<Name>(t *testing.T)`), invoked via `go test ./internal/testport/...`
or `-run TestPort_<Name>`. Each external client binary is resolved by
`clientToolBin(t, name)` (`client_tools_port_test.go`) → `exec.LookPath` then
`postgres/local_install/bin`; **missing binary ⇒ `t.Skip`** (so the suite is
safe to run without every PG tool installed).

### Entry-point families

| Family | Representative entry points | Ext. binary |
|--------|-----------------------------|-------------|
| TAP ports | `TestPort_Initdb001`, `TestPort_PgCtl001StartStop`/`002Status`/`003PromoteAdapted`, `TestPort_Pgbench001WithServer`, `TestPort_Psql001Basic` | initdb/pg_ctl/pgbench/psql |
| pg_dump / pg_dumpall | `pgdump_port_test.go`, `pgdumpall_parameter_acl_test.go`, `pgdump_role_config_test.go` | pg_dump (most-referenced) |
| pg_basebackup family | `pgbasebackup_port_test.go` | pg_basebackup/pg_receivewal/pg_recvlogical/pg_createsubscriber |
| pg_amcheck | `pgamcheck003/004_*`, `pgamcheck_btree_*` | pg_amcheck |
| pg_waldump | `wal_pg_waldump_test.go` → `TestPort_WALPgWaldumpCompat` | pg_waldump |
| recovery TAP | `TestPort_Recovery001StreamRep`, `013CrashRestart`, `047CheckpointPhysicalSlot` | pg_ctl/psql |
| subscription TAP | `TestPort_Subscription001RepChanges`, `004Sync`, `026Stats` | psql |
| **isolation** | `TestPort_IsolationSuite` + **121** `TestPort_Isolation*` | (in-process) |
| **regress** | `TestPort_RegressSuite` (**232** subtests) | psql |
| SSI / deferred-constraint / durability e2e | `ssi_write_skew_test.go`, `deferred_unique_e2e_test.go`, `set_constraints_e2e_test.go`, `*_durability_test.go` | (in-process/psql) |
| plpgsql / misc | `TestPort_PLpgSQL*`, `TestPort_PgStatActivity`, `TestPort_RecursiveCTE` | psql |

---

## C. Authoritative must-pass list — `docs/test-port/postgres-oracle-port-status.csv`

Loaded/validated by `internal/testport/framework/status.go`
(`LoadStatusCSV` / `ValidateStatusRows`). **This is the roll-up authority for
"which oracle tests must pass."**

Schema:
```
id,upstream_path,suite_type,status,pass_required,rationale,deferred_to
```
- `id` — unique (P-###=ported, D-###=deferred, E-###=excluded, plus WD-/AC- prefixes).
- `upstream_path` — must start with `postgres/` (validated).
- `suite_type` — tap | regress | isolation | mixed | modules | utility.
- `status` — **`port` | `defer` | `excluded`** (enum-validated).
- `pass_required` — **`yes` | `no`** (validated).
- `rationale` — required; for `port` it **names the `TestPort_*` func** to `-run`.
- `deferred_to` — milestone id; required when `defer`, must be empty/`-` when `excluded`.

`ValidateStatusRows` enforces unique id, `postgres/` prefix, status/pass_required
enums, non-empty rationale, `defer⇒deferred_to`, `excluded⇒no deferred_to`
(regression-tested by `framework/status_test.go::TestValidateRejectsDuplicateID`).

### Distribution (71 data rows)

| status \| pass_required | count | meaning for a batch |
|---|---:|---|
| `port` \| `yes` | **60** | **MUST run + pass now.** Rationale names the `TestPort_*`. |
| `port` \| `no` | 1 | ported, not gating |
| `defer` \| `no` | **8** | **promotable** — skip today, run when its milestone lands |
| `excluded` \| `no` | 2 | never run |

### The 8 promotable (`defer`) rows → unlock milestone

| id | prerequisite milestone (`deferred_to`) | area |
|----|----------------------------------------|------|
| D-001 | M0060-0002 | regress runner remainder |
| D-003 | M0094 | recovery TAP remainder |
| D-004 | M0094 | subscription TAP remainder |
| D-005l | M0060-0003 | connstr / CREATE DATABASE scripts |
| D-006 | M0060-0005 | test modules |
| D-007 | M0060-0005 | contrib |
| WD-002 | M0110-0002 | pg_waldump server tier |
| AC-003 | M0110-0003 | pg_amcheck remainder |

---

## D. Per-case granularity — `docs/test-port/postgres-oracle-target-inventory.csv`

900 rows — the per-spec expansion of the roll-up above. Schema:
```
suite_id,kind,item_path,source_pattern,status,rationale
```
Richer per-item status (this is where individual regress/isolation cases live):

| status | count |
|--------|------:|
| excluded | 261 |
| not-tried | 161 |
| pass | 144 |
| unknown | 110 |
| failed | 106 |
| defer | 75 |
| port | 43 |

`kind` buckets: regress 497, tap 172, isolation 121, mixed 110.
`suite_id` buckets: regress-expected 265, regress-sql 232, isolation-specs 121,
client-tools-tap 89, contrib-suites 63, modules-suites 47, recovery-tap 47,
subscription-tap 36. Rendered to `.md` by `cmd/gen-oracle-inventory` (renders
only; never rewrites the CSV — it is the authority).

---

## E. Regress & isolation coverage + numeric baselines

- **Regress runner:** `internal/testport/framework/regress.go`
  (`RunRegressSubset`, `DiscoverRegressCases`) + `regress_suite_test.go`
  (`TestPort_RegressSuite`). Boots a live goopg cluster; runs each of 232
  SQL/expected pairs as a subtest via `psql -X -q -a -f`; maps
  `port→run`, `excluded→Skip`, `defer→Skip`. Needs `psql`.
  - Coverage doc: `docs/test-port/upstream-regress-coverage.md`
    (`cmd/gen-regress-coverage`) — 232 discovered.
  - **Diff baseline:** `docs/test-port/regress-diff-baseline.csv`
    (schema `name,diff_lines,status`; 128 rows — 127 `pass`, 1 `excluded`).
    This is the per-test diff-line regression baseline (e.g. `advisory_lock,0,pass`).
- **Isolation:** `internal/testport/isolation_port_test.go`
  (`TestPort_IsolationSuite` + 121 per-spec). Coverage doc
  `docs/test-port/upstream-isolation-coverage.md`
  (`cmd/gen-isolation-coverage`) — **121 specs, 120 pass, 1 failed
  (`deadlock-parallel.spec`)**. Level mapping in
  `docs/test-port/executable-isolation-tests.md`.
- **TAP:** `docs/test-port/upstream-tap-coverage.md` (`cmd/gen-tap-coverage`).

---

## F. The "green today / promotable later" model (what a batch must implement)

1. **Run now:** the CI Go unit set (§A) + the 60 `port/yes` oracle rows (§C) +
   the 120 passing isolation specs and 127 passing regress cases (§E).
2. **Skip but track:** the 8 `defer` rows (§C) and the `failed`/`not-tried`
   per-case rows (§D). Each carries a `deferred_to` milestone or a rationale.
3. **Promote when it flips to pass:** edit the CSV (`status`→`port`/`pass`,
   `pass_required`→`yes`, fill `rationale` with the Go func name, clear
   `deferred_to`), then regenerate the rendered `.md` via the matching
   `cmd/gen-*` tool (see `02-execution-scripts.md`). This keeps the batch's
   must-pass set data-driven — **read the CSV at run time, don't hard-code.**
