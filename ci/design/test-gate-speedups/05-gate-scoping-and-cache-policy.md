# 05 — Gate Scoping and Test-Cache Policy

Status: draft. Part of [test-gate-speedups](README.md).

Doc [01 §2.1](01-where-time-goes.md) showed the Go test result cache is
already the dominant speed lever (units 2412 s → 307 s; race 3577 s → 442 s)
— by accident. This doc makes it policy (§1), then evaluates the more
aggressive lever, running *fewer* tests per commit (§2), and closes with the
smoke-gate policy (§3).

## 1. Cache-warmth rules (normative; zero risk; adopt immediately)

The Go toolchain caches per-package test *results* keyed by the test binary
and its inputs; an unchanged package re-"passes" from cache in milliseconds.
Rules for every gate and every prompt that describes gates:

1. **Never pass `-count=1` in a gate invocation.** It defeats the entire
   cache and turns a 5-minute `units` run into a 40-minute one. `-count=1`
   is for *validation* runs where you must prove the test executes (flake
   screens, probe measurements — e.g. [04 §2.4](04-parallelism-and-bootstrap-caching.md),
   [03 §5](03-tmpfs-data-dirs.md)), never for the gate itself.
2. **A cached PASS is a real PASS — within the key's coverage.** The cache
   key covers the test binary, its source, GOFLAGS, environment variables the
   test process reads, and files the test process opens or stats. If none
   changed, re-executing is pure waste. The precise limits:
   - The key does NOT cover network, wall-clock, binaries exec'd **by
     explicit path** (no `LookPath` ⇒ no stat recorded), or any file read
     only by a **subprocess**. Server-backed suites like testport shell out
     to `psql`/`pgbench` and a goopg server; they re-run each night not
     because of `-v` (which is a cacheable flag) but because their inputs —
     notably the freshly built goopg binary the harness stats — change every
     build.
   - The unit-gate package set is *mostly* hermetic but not entirely:
     `internal/wal/pg_waldump_compat_test.go` execs the real `pg_waldump`
     (via `exec.LookPath`, so the binary at least enters the key as a stat).
   - **Rule 2a (normative):** a unit-gate test must not exec a binary by
     explicit path or depend on files only a subprocess reads; where
     unavoidable, the test must itself open/stat those inputs so they enter
     the cache key. Enforce in review — this is what keeps "cached PASS is a
     real PASS" true rather than folklore.
3. **Don't gratuitously vary cache keys.** A gate wrapper that injects a
   changing env var (timestamps, run IDs) into `go test`'s environment, or
   flips `GOFLAGS` between runs, silently invalidates everything. New gate
   plumbing must keep the `go test` command line and environment
   byte-identical run-to-run. (This is why the tmpfs `TMPDIR` export of
   [03 §3.3](03-tmpfs-data-dirs.md) uses the FIXED path
   `/dev/shm/goopg-gate-tmp` rather than a per-run suffix: `TMPDIR` enters
   the cache key of every test that calls `t.TempDir()`, so a changing value
   would cold-start the cache on each gate run.)
4. **`-race` is a separate cache dimension** (different build): warming one
   does not warm the other. Expected; not a bug.
5. **CI keeps `setup-go` cache enabled** (already true, `test.yml:66`).

Rule 3's implication for doc 03 is intentional self-criticism: cache policy
constrains how the other proposals are implemented.

## 2. Affected-package selection (proposed; ranked LAST — highest quality risk)

### 2.1 Mechanism

Run, per commit, only packages whose tests could see the change: the changed
packages plus everything that (transitively) imports them, computed from the
import graph:

```bash
#!/usr/bin/env bash
# affected-packages.sh — emit the go-test package set affected by a diff.
# PROPOSED (design doc only).
#
# Usage: affected-packages.sh <git range>   # range is REQUIRED and pinned by
# the caller: the pre-commit caller passes HEAD (worktree diff), a
# post-commit caller passes HEAD~1..HEAD — an implicit default would make
# the two call sites silently select different sets.
#
# FAIL-OPEN CONTRACT: every ambiguous case widens to the FULL package set.
# The failure mode to design against is emitting too little, never too much.
set -euo pipefail
range="${1:?usage: affected-packages.sh <git range>}"

full_set() { go list ./... ; exit 0; }

all_changed=$(git diff --name-only "$range")
# Nothing changed at all -> genuinely nothing to test (doc-only is handled
# below, this is the empty-diff case).
[ -z "$all_changed" ] && { echo "affected-packages.sh: empty diff — no packages" >&2; exit 0; }

# Pure-documentation paths are the ONLY thing allowed to select nothing:
# they are provably not build or test inputs. Everything else non-.go widens.
doc_re='(\.md$|^docs/|^analysis/|^ci/design/)'
nondoc_changed=$(echo "$all_changed" | grep -vE "$doc_re" || true)
if [ -z "$nondoc_changed" ]; then
  echo "affected-packages.sh: doc-only diff — no packages (smoke hook still runs)" >&2
  exit 0
fi

# Auto-widen triggers — checked FIRST, before any selection logic:
#  (a) ANY non-doc, non-.go change: embedded assets (go:embed of
#      postgresql.conf.sample, initdb bootstrap data), testdata, build
#      plumbing — the import graph sees none of these. Simple and safe: widen.
#  (b) build/format-bearing .go paths (REAL paths — there is no internal/codec;
#      the heap codec lives in internal/executor, its WAL twin in internal/wal).
#  (c) breadth: >8 changed packages.
if echo "$nondoc_changed" | grep -qv '\.go$'; then
  echo "affected-packages.sh: auto-widen (non-.go change)" >&2; full_set
fi
if echo "$nondoc_changed" | grep -qE \
  '^internal/(catalog|storage|wal|access|config|initdb|mvcc|pglz|executor)/'; then
  echo "affected-packages.sh: auto-widen (format-or-build-bearing path)" >&2; full_set
fi

# changed .go files -> their packages; a path go list cannot resolve
# (deleted package, moved dir) widens rather than vanishing.
changed_pkgs=""
while read -r d; do
  [ -z "$d" ] && continue
  if p=$(go list "./$d" 2>/dev/null); then
    changed_pkgs="$changed_pkgs $p"
  else
    echo "affected-packages.sh: auto-widen (unresolvable changed path $d)" >&2; full_set
  fi
done < <(echo "$nondoc_changed" | xargs -r -n1 dirname | sort -u)

[ "$(echo $changed_pkgs | wc -w)" -gt 8 ] && { echo "affected-packages.sh: auto-widen (>8 packages)" >&2; full_set; }
# Changed files existed but no package resolved -> ambiguous: widen.
[ -z "${changed_pkgs// /}" ] && { echo "affected-packages.sh: auto-widen (no package resolved)" >&2; full_set; }

# Reverse-import closure over the TEST-EXPANDED graph. Plain {{.Deps}} is
# test-blind: it excludes TestImports/XTestImports, so a package whose
# *tests* import a changed package would never be selected (real escapes
# exist in this repo: internal/analyzer's tests import internal/planner
# test-only). The synthetic test packages' .Deps ARE complete; .ForTest maps
# them back to the tested package.
go list -test -f '{{if .ForTest}}{{.ForTest}}{{else}}{{.ImportPath}}{{end}} {{join .Deps " "}}' ./... \
  | awk -v pkgs="$changed_pkgs" '
      BEGIN { n=split(pkgs, a, /[[:space:]]+/); for(i=1;i<=n;i++) if(a[i]!="") want[a[i]]=1 }
      { for (i=2; i<=NF; i++) if ($i in want) { print $1; next }
        if ($1 in want) print $1 }' | sort -u
```

Caller contract: intersect the output with the gate's `EXCLUDE` list; if the
intersection is **empty**, print "no affected packages (smoke hook still
runs)" and **exit 0 without invoking `go test`** — the existing gate
correctly refuses an empty package list as falsely-green
(`ralph-precommit-test.sh:72-75`), so handing it an empty set would turn
every doc-only commit into a red gate.

### 2.2 Why it is ranked last

The import graph misses real coupling; each miss class needs an explicit
answer:

| escape | example | answer |
|--------|---------|--------|
| test-only imports | `internal/analyzer`'s tests import `internal/planner` test-only; planner isn't in the widen regex, so a planner change would miss analyzer's tests under a `.Deps` closure | the closure runs over the test-expanded graph (`go list -test` + `.ForTest` mapping, §2.1) — this class is closed by construction, not by widening |
| non-`.go` build inputs | `go:embed` of `postgresql.conf.sample` or initdb bootstrap data; testdata; go.mod; scripts | auto-widen on any non-doc non-`.go` change, §2.1 |
| on-disk-format coupling without imports | codec change breaks a testport row that only talks wire protocol | auto-widen on format-bearing paths (real ones: `catalog|storage|wal|access|config|initdb|mvcc|pglz|executor`), §2.1 |
| deleted/moved packages | importers of a removed package never selected | unresolvable changed path ⇒ widen, §2.1 |
| build tags / generated code | `//go:build integration` files invisible to plain `go list` | `-tags`-aware listing in the implementing loop; integration suites are outside the unit gate anyway |
| behavioral coupling through a live server | dispatch change surfaces only in pgbench | the smoke hook still runs on EVERY commit, unconditionally — selection never touches it |
| everything else | unknown unknowns | the nightly full batch is the backstop — see the honest framing below |

**On the backstop, honestly:** "worst-case detection latency is one day" only
holds against a *green* nightly where a new failure stands out. The recent
baseline is red every night with ambient regressions ranging 1–96
(`ci/logs/history.jsonl`), so an escaped regression lands in an
already-red pile routed through M-NIGHTLY triage — realistic detection is
days, and attribution is a bisect. Therefore stage 6 carries an explicit
**precondition**: the nightly must first have a sustained-green (or
fully-per-test-triaged) baseline so that one new red is one new signal.
Until then this proposal must not be adopted at all.

With a warm cache the honest marginal win is minutes, not tens of minutes —
the cache already skips unchanged packages' *execution*, selection only skips
their (fast) cache probes plus the build-graph walk. The big win appears
exactly when the cache is cold (branch switch, toolchain bump) — which is
also when selection's risk is highest. Hence: **optional, stage 6, adopt only
if the stage 1–4 levers prove insufficient AND the precondition above
holds.** This mirrors the project's history: the pgbench-smoke hook exists
precisely because targeted testing let a concurrency regression class
through — selection re-opens a smaller version of that hole, and must never
be allowed to skip the smoke.

## 3. Smoke-gate policy: keep it on every commit; make it fast rather than rare

Options considered for the ~2–3 min per-commit hook:

| option | verdict |
|--------|---------|
| shorten `-T 30` windows | **rejected** — the three windows are the perf-tripwire measurement itself; shorter windows raise variance exactly where the gate exists to detect drift (TPC-B concurrency regressions). The `-P 5` progress lines from a 10 s window are statistically useless. |
| skip on doc-only commits | **rejected for automation** — classifying "doc-only" safely is the affected-selection problem again (§2), on the highest-blast-radius gate; the existing manual escape (`GOOPG_SKIP_PRECOMMIT=1`) already covers the human-judgment case and leaves an audit trail in the shell history, not in an easily-fossilized classifier |
| make the fixed overhead cheap | **adopted** — `--no-sync` init ([02 A.1](02-durability-off-for-test-servers.md)), tmpfs data dir ([03 §3.2](03-tmpfs-data-dirs.md)), and the already-warm build cache cut everything *around* the 90 s of measurement; realistic floor ≈ 100–120 s total |

The hook's value is its unconditionality (see the blind-spot history in
`scripts/ralph-precommit-test.sh:51-53`); this bundle deliberately optimizes
within that constraint instead of negotiating with it.
