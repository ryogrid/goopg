# 04 — Bootstrap Template Caching and `t.Parallel()` Pilots

Status: draft. Part of [test-gate-speedups](README.md).

Two structural changes to the test harness itself: stop re-running identical
initdb bootstraps (§1), and stop running I/O-wait-heavy tests one at a time
(§2). Both are test-only changes (no production code), but they are the two
proposals most able to *create* flakiness if done carelessly, so each carries
an acceptance gate.

## 1. initdb template caching (upstream model: PG 16 `INITDB_TEMPLATE`)

### 1.1 Precedent

PostgreSQL 16 added exactly this to its own harness: when the environment
variable `INITDB_TEMPLATE` points at a pre-initialized data dir, node
creation **copies** it instead of running initdb
(`postgres/src/test/perl/PostgreSQL/Test/Cluster.pm:640-667` — initdb runs
only when custom initdb options are requested or the template is absent).
The meson build initializes the template once per test run.

### 1.2 goopg design

A full `goopg init` produces a deterministic ~9 MB tree given the same
options. Today it runs ~178× in `internal/initdb` tests and 351× across
testport. Cache it per test process:

```go
// internal/testutil/cluster/template.go (sketch)

var (
	templateMu   sync.Mutex
	templateDirs = map[string]string{} // key: canonicalized init-args -> template dir
)

// templateFor returns a data dir template initialized with exactly args,
// creating it on first use. Templates live under os.TempDir() (so they land
// on tmpfs when TMPDIR does — doc 03) and are keyed by the init argument
// list plus the goopg command identity (GoopgCommand / binary path — uniform
// today, cheap to include); any difference gets its own template.
//
// Refusals (fall back to a direct init, never cache):
//   - arg sets containing -X/--waldir: init then creates pg_wal as a symlink
//     to ONE absolute WAL dir (initdb.go:436-458); a copied template would
//     make every clone write the SAME physical WAL directory.
//   - any symlink encountered during the copy (defense in depth): the copy
//     must WalkDir without following links and refuse rather than replicate.
func templateFor(repoRoot string, args []string) (string, error) {
	key := strings.Join(args, "\x00")
	templateMu.Lock()
	defer templateMu.Unlock()
	if dir, ok := templateDirs[key]; ok {
		return dir, nil
	}
	dir, err := os.MkdirTemp("", "goopg-initdb-template-")
	if err != nil {
		return "", err
	}
	// run `goopg init -D dir args...` once (always with --no-sync: the
	// template is throwaway by definition)
	...
	templateDirs[key] = dir
	return dir, nil
}

// (*Cluster).Init() then becomes: copy templateFor(...) into c.dataDir
// (filepath.WalkDir copy, no link-following), then RE-IDENTIFY the clone,
// instead of running goopg init. SyncInit: true (doc 02 A.2) BYPASSES the
// template entirely and runs a direct, fully-synced init — durability tests
// must exercise the real init path.
```

**Re-identification is mandatory, not optional.** `goopg init` mints a random
cluster **system identifier** (`LoadOrCreateSystemID`, persisted in
`global/system_identifier`, embedded in `pg_control` and in every
PG-compatible WAL page header, where pg_waldump cross-checks it). Naive
copies would give every test cluster in a process the SAME sysid, which
(a) blinds the pg_waldump cross-check ports to wrong-cluster WAL forever, and
(b) removes the sysid-mismatch rejection path from every test that pairs two
independently-created clusters (failover/replication E2Es, PG-standby-attach)
— a harness or product data-dir/WAL cross-wiring bug would become
structurally invisible. (Upstream `INITDB_TEMPLATE` also shares sysids, but
upstream's paired nodes come from basebackup; goopg's ports create
independent clusters, so goopg cannot inherit that shortcut.) The copy path
must therefore re-randomize `global/system_identifier` and rewrite the
dependent surfaces (`pg_control`, the initial WAL segment's long page
header) — reusing the same code `goopg init` uses to stamp them. If that
rewrite proves fiddly in practice, the fallback is NOT to accept shared
sysids: it is to exclude the replication/basebackup/waldump port families
from the template (direct init, like `SyncInit`) and record that exclusion
in the harness.

Note on lifetime: template dirs are `os.MkdirTemp`, not `t.TempDir`, so no
test cleans them — a crashed test process leaks its template until the
shared-`TMPDIR` age sweep ([03 §3.3](03-tmpfs-data-dirs.md)) collects it;
on disk-backed `os.TempDir()` they persist until the OS tempdir hygiene.
Acceptable (~9 MB each, few per process), but the implementing loop should
register a `TestMain` cleanup as politeness.

The initdb package tests get the same via a small `mustInitFromTemplate(t,
opts)` helper for call sites whose Options match a cacheable shape; tests
that exercise init *itself* (error paths, checksum bootstrap, locale
variants) keep calling `Init` directly — they are the feature under test.

### 1.3 Invalidation analysis

The classic failure mode of bootstrap caches is staleness after a catalog
format change. This design is immune **by construction**:

- The map is **per test process**. Every `go test` invocation starts a fresh
  process, so a rebuilt `goopg` binary can never see a template made by an
  older binary. Nothing persists across runs; there is no cross-run cache to
  invalidate.
- Templates are keyed by the **full init argument list**, so option-sensitive
  tests can't collide.
- Cost when cold: exactly one init per (process × distinct arg-set) — the
  status quo's per-test cost becomes the worst case, never exceeded.

The trade-off of per-process scoping: `go test ./...` runs each package in
its own process, so the template is rebuilt once per package (fine — that's
≤ a handful of inits), and testport's single process amortizes 351 → 1.
A cross-run persistent template (keyed by binary build ID) would save a
further ~0.1–0.8 s per package but adds a real staleness surface; not worth
it — explicitly rejected.

### 1.4 Correctness guard

One new test: init a dir directly, init another via the template copy with
identical args, and require identical file listings + per-file checksums.
Empirically a default `goopg init` embeds no data-dir path in any file
(verified by grep over a probe init), so the exclusion list is short and
known: `pg_control`, `global/system_identifier`, and the initial WAL segment
— i.e. **exactly the re-identification surface of §1.2, which is also
exactly where a copy bug would hide**. The test must therefore not merely
skip those files: it enumerates each exclusion in the test source with a
justification comment, and applies a structural check instead of a byte
check — parse `pg_control` and the WAL long page header from both dirs and
require every field EXCEPT the sysid/timestamps to be equal, and require the
clone's sysid to be *different* from the template's (pinning §1.2's
re-randomization). A byte-identical-except-list guard that silently excludes
its own risk surface would pass by construction and certify nothing.

## 2. `t.Parallel()` pilots

### 2.1 Why pilots, not a sweep

Marking 3,000 tests parallel in one loop is un-reviewable and un-bisectable.
Each pilot = one package, one loop, with the hazard checklist applied and the
acceptance gate below. Order by ROI:

| order | package | tests / parallel today | expected shape |
|-------|---------|------------------------|----------------|
| 1 | internal/initdb (~223 s) | 625 / 3 | I/O-wait-heavy (fsync, file trees): parallelism hides latency even before docs 02/03 land; with them it becomes CPU-bound and still helps |
| 2 | internal/executor | 1339 / 0 | CPU-bound: bounded win (~cores), biggest package |
| 3 | internal/catalog | 256 / 0 | mixed |

`internal/testport` is a *fourth* candidate with the largest absolute win
(hundreds of serial server boots) — but two binding constraints gate it:
memory (N concurrent servers × footprint inside the nightly 6G/8G scope),
and the port allocator: `freePort()` (`cluster.go:564-579`) is
listen-`:0`-close-return, a textbook TOCTOU that serial execution has been
masking — between the probe's `Close()` and the server's bind, a parallel
test, the kernel's ephemeral allocator, or a co-loaded gate on the same host
can take the port. The testport pilot therefore requires bind-retry-with-a-
fresh-port in the cluster harness (treat "address already in use" at startup
as retryable, N attempts) *before* any `t.Parallel()` lands there. Staged
after the unit-package pilots.

### 2.2 Hazard checklist (run per pilot, greps included)

| hazard | detection | resolution |
|--------|-----------|------------|
| `t.Setenv` — Go panics if combined with `t.Parallel()` | `grep -rn 't\.Setenv' internal/<pkg> --include='*_test.go'` | leave those tests serial (Go enforces it; the panic is immediate and loud) |
| `os.Setenv` / package-level mutable globals | `grep -rn 'os\.Setenv\|^var ' <pkg> --include='*_test.go'` + review | serialize or refactor to per-test state |
| fixed ports / fixed paths | `grep -rn '127\.0\.0\.1:[0-9]\|"/tmp/' <pkg>` plus a review of `os.TempDir()` uses — the narrow `/tmp/goopg` pattern misses real offenders (e.g. `internal/testport/profile_update_test.go:34` writes `/tmp/update.pprof`; the project memory records `/tmp/TestPort_*` pollution) | switch to `freePort()` / `t.TempDir()` |
| the shared in-memory catalog singleton | review: tests constructing/mutating package-level catalog state (per-connection virtual scoping exists, but tests may reach the shared object directly) | per-test catalog instances, or serialize |
| package-level GUC registry mutation | `grep -rn 'Registry\|SetGUC\|defaults\.' <pkg> --include='*_test.go'` + review | per-test registries, or serialize |
| global process state (chdir, umask, rlimits) | review | keep serial |
| shared fixture dirs written by multiple tests | grep fixture paths | copy-on-use |
| memory amplification under cgroup caps | arithmetic: N × per-test footprint vs scope limit | cap with explicit `-parallel` (below) |

### 2.3 Memory discipline: explicit `-parallel`, mirroring the `-p=4` precedent

The nightly already caps **cross-package** parallelism to protect the 32 GiB
host (`GOFLAGS=-p=4`, `ci/batch/stages/stage-units.sh:29`,
`stage-race.sh:15`). Intra-package parallelism multiplies with it (worst case
`-p × -parallel` concurrent tests), so pilots must set an explicit bound in
gate invocations rather than inheriting the default (`-parallel` defaults to
`GOMAXPROCS`):

```bash
go test -parallel 4 ./internal/initdb/...
```

and the nightly stages keep `NIGHTLY_GO_P` semantics unchanged. Start at 4;
raise only with measured RSS evidence per package.

### 2.4 Acceptance gate per pilot

A pilot lands only when, in the same loop:

1. `go test -count=1 ./internal/<pkg>/` green **10× consecutively** (flake
   screen — `-count=1` here is a *validation* run, not a gate default; see
   [05 §1](05-gate-scoping-and-cache-policy.md)).
2. `go test -race -count=1 -parallel 4 ./internal/<pkg>/` green — parallel
   tests are precisely where the race detector earns its keep.
3. Package wall-clock before/after recorded in the pilot's commit message.
4. Any test left serial gets a one-line comment saying why (`t.Setenv`,
   global state, ...), so the next sweep doesn't "fix" it blindly.

Honest math on step 1: 10 consecutive greens detect a 5%-per-run flake with
probability 1 − 0.95¹⁰ ≈ **40%** (a 1% flake: ~10%). 10× is the in-loop bar,
not the acceptance proof. A pilot is *accepted* (and stays landed) only
after a soak: total executions ≥ 60 within the loop (e.g. `-count=6` × the
10 runs, or additional `-count=` batches — ~95% detection of a 5% flake)
**plus** the next 3 nightlies watched with rollback on any
parallelism-attributable flake. Given the standing rule that a flaky shared
suite blocks every loop, under-screening here is how this proposal would
destroy more time than it saves.

### 2.5 Quality-risk framing

`t.Parallel()` doesn't weaken assertions — every test still runs. The risk is
**new flakiness from latent shared state**, i.e. the checklist hazards
escaping review. Two honest observations: (a) a test that fails under
parallelism usually reveals a real test-hygiene bug worth fixing anyway; (b)
the goopg *server* is inherently concurrent, so intra-package parallel tests
mildly increase concurrency coverage, not decrease it. The 10×-green +
race-gate acceptance keeps the flake cost from landing on unrelated loops —
the project's standing rule that a flaky suite blocks everyone
(AGENT.md pre-commit section) is exactly why pilots must not merge red or
jittery.
