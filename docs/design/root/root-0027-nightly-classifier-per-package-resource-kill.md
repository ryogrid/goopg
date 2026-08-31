# root-0027 — Nightly classifier: per-package resource-kill vs. regression (M-NIGHTLY follow-up)

## Context

Three consecutive M-NIGHTLY triage loops (deferral ledger, task-id `M-NIGHTLY
(run 20260715-010036 triage)`) left `cmd/goopg`/`internal/amcheck`/
`internal/mvcc` as an open, unconfirmed mystery: each hit the nightly's
33-minute per-package `go test` timeout during the `units` stage, and repeated
attempts to reproduce a hang under quiet-host or lightly-loaded conditions
failed (`internal/initdb`, a fourth package flagged the same way, reran clean
standalone in ~4 minutes). The sibling `internal/wal` item from the same
nightly run turned out to be a genuine, fixable test-scheduling bug
(`docs/design/0107-0011-wal-drain-invariant-test-scheduling-artifact.md`), so
the working hypothesis for the remaining three stayed open rather than
dismissed.

This loop reproduced the failure directly and traced it to something other
than a product hang: a classification bug in the nightly batch's own
summarizer, `ci/batch/lib/summarize.py`.

## Reproduction

Ran the full `units` package set through `scripts/goopg-test-run.sh` with the
**exact** nightly cgroup configuration from `ci/batch/stages/stage-units.sh`:

```
GOOPG_CG_UNIT=<unique> GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G GOOPG_MEM_SWAP_MAX=0 \
GOMEMLIMIT=5GiB scripts/goopg-test-run.sh env GOFLAGS=-p=4 \
go test -timeout 15m $(go list ./... | grep -vE '<CI exclude list>')
```

All four previously-flagged packages (`cmd/goopg`, `internal/amcheck`,
`internal/initdb`, `internal/mvcc`) failed **identically** — in 16.5 minutes,
faster than the nightly's own 33-minute timeout. `internal/initdb` had
already been proven clean when run standalone; reproducing it here only under
the concurrent, memory-capped configuration confirms this is genuine resource
contention from running ~40 packages concurrently under `GOMEMLIMIT=5GiB`/
`MemoryMax=8G`, not a per-package flake, and not sensitive to the exact
`-timeout` value (it reproduces well before 33 minutes once the real
contention is present).

## Two distinct failure signatures

Comparing both the original nightly log
(`ci/logs/20260715-010036/units/go-test.log`) and this loop's reproduction
log package-by-package:

- **`cmd/goopg` / `internal/amcheck`**: die via a bare `signal: killed`, with
  no SIGQUIT goroutine dump at all (`cmd/goopg`) or a truncated one-line dump
  (`internal/amcheck`, just `SIGQUIT: quit` / `PC=...` then nothing). This is
  the classic signature of a hard SIGKILL that gives the process no chance to
  unwind — consistent with `ci/design/03-resources-and-parallelism.md` §C's
  documented "resource-kill" classification: either the scope's own
  `MemoryMax` cgroup-OOM or the host-level OOM killer, both to be treated as
  `resource-kill → inconclusive`, never a product regression.
- **`internal/initdb` / `internal/mvcc`**: produce a **full** SIGQUIT
  goroutine dump from Go's own `-timeout` mechanism — dozens of idle GC
  workers plus exactly one goroutine `running` with `stack unavailable`
  (`internal/mvcc`'s dump: goroutine 1 parked in `testing.(*T).Run`'s
  `chanrecv`, goroutine 2085 `[running]`, ~20 `GC worker (idle)` goroutines).
  Notably, **neither block contains the literal text `signal: killed`** —
  only `*** Test killed with quit: ran too long (33m0s).` followed directly
  by the package's `FAIL` summary line. This is a materially different,
  still-ambiguous signature: it could be genuine heavy GC-assist pressure
  from operating near `GOMEMLIMIT` (the "running-with-unavailable-stack" +
  "many idle GC workers" pattern is consistent with that), or it could be a
  real product deadlock that would look identical from the log alone. The
  classifier correctly does **not** auto-suppress this signature — it stays
  a "regression" AI item requiring a human/agent to actually read the dump.

## Root cause: a whole-log classification bug, not a product bug

`summarize.py`'s existing rule (already correctly designed, per
`ci/design/02-test-selection.md` §B: `signal-9 kill with no test FAIL ->
resource-kill (inconclusive)`) was:

```python
log = read_file(os.path.join(run_dir, stage, "go-test.log"))
if looks_resource_killed(log) and "--- FAIL" not in log:
    it.resource_kills.append({"stage": stage, "evidence": ev(...)})
    continue
_, _, failed_pkgs = parse_go_test(log)
for pkg in failed_pkgs[:10] or ["(suite)"]:
    it.add_regression(...)   # every FAIL <pkg> line, unconditionally
```

Both checks ran over the **entire** combined `units`/`race` log — every
package in the stage sharing one `go-test.log` file, since `go test` runs
with `GOFLAGS=-p=4` across ~40 packages in one invocation. The night in
question, `internal/wal` had one genuine `--- FAIL:
TestStripeAppendConcurrentDrainConsistency` (a real bug, since fixed — see
root doc 0107-0011) *somewhere* in that same combined log. That single
substring match flipped `"--- FAIL" not in log` to `False` for the **entire**
stage, so the classifier fell through to the regression branch and reported
`cmd/goopg`'s and `internal/amcheck`'s pure resource-kills as regressions
right alongside `internal/wal`'s real bug and `internal/initdb`/
`internal/mvcc`'s ambiguous timeouts — indistinguishable in
`ci/logs/action-items.md`.

**The same bug, inverted, was also silently swallowing real information in
the `race` lane.** The original nightly's `race/go-test.log` had exactly one
`signal: killed` (from `race/cmd/goopg`) and **zero** `--- FAIL` occurrences
anywhere in the whole log — so the whole-log check fired `True`, and the
**entire** `race` stage's three failing packages (`cmd/goopg`,
`internal/access/btree`, `internal/amcheck` — all ~54 minutes, essentially
simultaneous) collapsed into one generic, non-blocking "Resource kills"
summary notice. `internal/access/btree`'s and `internal/amcheck`'s `-race`
failures never appeared in `action-items.md` on any prior night — a package
with a real history of concurrency bugs (M0110-0007's split prev-link race)
was silently going unreported under `-race`.

## Fix

`ci/batch/lib/summarize.py`:

- New `split_go_test_pkg_blocks(log_text)`: splits a `go test` (non `-v`) log
  into per-package `(pkg, status, block_text)` tuples, using the `ok`/`FAIL`/
  `?` package-summary lines as block boundaries (each block accumulates every
  line since the previous boundary, so a kill signature or assertion dump is
  attributed to the package that produced it).
- The `units`/`race` classification loop now runs `looks_resource_killed`/
  `"--- FAIL" not in text` **per package block** instead of once over the
  whole log. A package with only a resource-kill signature in its own block
  goes to `resource_kills` (now tagged with the attributed `pkg`); a package
  whose own block contains a real `--- FAIL` goes to `regressions`, exactly
  as before but no longer contaminated by an unrelated package's failure
  mode.
- The "Resource kills" summary render (`summary.md`) now includes the
  attributed package when present.
- Fallback preserved: if a stage is marked `fail` but no per-package `FAIL`
  summary line is found at all (e.g. the whole run died before any package
  finished), the classifier falls back to the old whole-log signal.

New `ci/batch/lib/test_summarize.py` (stdlib `unittest`, no third-party
deps): a synthetic fixture modeled on the real 2026-07-15 log (one genuine
`--- FAIL` package alongside two pure-`signal:-killed` packages) proves the
two classes no longer bleed into each other in either direction, plus a
pure-resource-kill-only case. `split_go_test_pkg_blocks` and the full
`analyze()` path were both additionally cross-checked directly against the
real `ci/logs/20260715-010036/units/go-test.log` and `race/go-test.log`
before committing, confirming the fix's effect on production data (not just
the synthetic fixture) — see the fix_plan.md M-NIGHTLY entry for the exact
before/after `action-items.md` diff.

## Verification

- `python3 ci/batch/lib/test_summarize.py -v` — 4/4 PASS.
- `python3 -m py_compile ci/batch/lib/summarize.py` clean.
- Manually re-ran `summarize.py --run-dir ci/logs/20260715-010036 --repo-root
  .` against the real historical logs: `cmd/goopg`/`internal/amcheck` (units)
  correctly move from `regressions` to `resource_kills`;
  `race/internal/access/btree`/`race/internal/amcheck` correctly newly appear
  as `regressions` (previously invisible); `internal/wal`/`internal/initdb`/
  `internal/mvcc`/the 3 isolation specs/the 3 `regress/*` cases are
  unaffected (all independently already known-resolved or already-open per
  the deferral ledger). `ci/logs/action-items.md` regenerated from this real
  run and committed (this file is explicitly "regenerated by every nightly
  batch run" per its own header); the incidental duplicate
  `ci/logs/history.jsonl` append this manual re-run produced was reverted
  (`git checkout`) — that file is append-only per real nightly run and must
  not gain a phantom entry from a manual verification invocation.
- This is a CI-tooling-only change (no Go/product code touched), but the
  pgbench smoke still ran per policy: `scripts/tpch-spotcheck.sh` PASS
  (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke bash
  scripts/ralph-precommit-test.sh` PASS (0 failed, all 3 workloads, via the
  pre-commit hook).

## Remaining open work

- `internal/initdb`/`internal/mvcc` (units) and `race/internal/access/btree`/
  `race/internal/amcheck` (race, newly surfaced by this fix) still show only
  the ambiguous SIGQUIT-timeout-without-`signal:-killed` signature. The
  classifier correctly leaves these as regression items pending actual
  investigation — this fix makes the log honest, it does not resolve
  whether these four are resource-starvation artifacts (this loop's working
  hypothesis for `internal/mvcc`, based on its "one goroutine running + many
  idle GC workers" dump pattern) or genuine hangs. `race/internal/access/
  btree`/`race/internal/amcheck` were never previously investigated at all
  (new fix_plan.md M-NIGHTLY tasks, unstarted) and deserve real attention
  given `internal/access/btree`'s history of real concurrency bugs.
- The `testport` stage (`summarize.py` lines ~199-212) has a narrower,
  structurally different variant of the same whole-log-vs-per-unit masking
  pattern (its resource-kill check also runs over the whole `testport` log),
  but testport failures are already individually attributed via its `-v`
  output (`parse_go_test`'s per-test `--- FAIL`/`PASS` results), so a
  resource-kill co-occurring with a real testport failure loses only the
  resource-kill notice, not a false regression — lower priority, not fixed
  this loop.
