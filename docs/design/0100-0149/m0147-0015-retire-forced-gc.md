# M0147-0015 — retire `GOOPG_FORCED_GC` (commit-path forced GC)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 1. Lever count 26 → 25
(`scripts/lever-count.sh`).

## What was retired

`maybeForceGCAfterCommit` (`internal/postmaster/dispatch.go`) could run `runtime.GC()` + `debug.FreeOSMemory()`
after a commit. It ran when HeapInuse was past 4 GiB, checked every 10 000 queries. M0061-0004 added it after a WSL2
memory incident. A later user request switched it off by default behind `GOOPG_FORCED_GC=on|true|1|yes`.

PG has nothing like it: backend memory is released by memory-context reset/delete at transaction and query end
(`postgres/src/backend/utils/mmgr/mcxt.c`), not by a periodic process-wide collection. goopg's equivalent is the Go
runtime's own pacing (`GOGC` / `GOMEMLIMIT`), which the benchmark lanes already set explicitly (see `CLAUDE.md`,
"Heavy queries at S-cold need GC headroom"). The lever was goopg-private and default OFF, so phase 1 deletes it.

## Removed

`internal/postmaster/dispatch.go`:

- the switch: `forcedGCEnabledFlag`, its `init` env read, `parseForcedGCEnv`, `SetForcedGCEnabled`, `forcedGCEnabled`;
- `maybeForceGCAfterCommit`, with `heapReleaseThresholdBytes`, `queriesWithoutFreeCounter` and
  `queriesPerForcedFree`;
- its autocommit call site, including the `ectx.DidWrite()` guard that existed only to keep `pgbench -S` off it;
- the `os`, `runtime`, `runtime/debug` and `sync/atomic` imports, which nothing else used.

Other call sites: two in `copy.go` (COPY FROM's CopyDone paths) and one in `txn_verb.go` (explicit COMMIT).

`internal/executor/context.go`: `(*Context).DidWrite`. Its doc comment and its only caller described one purpose,
gating the forced GC. `deadcode` does not flag an exported method, so `git grep` confirmed no remaining caller.

`deadcode ./cmd/...` was diffed before and after. The only new entry was `SetForcedGCEnabled`, which the test used
and which is now deleted.

`docs/wiki/modules/postmaster.md`: the "GC throttling" and "GC counter tuning" sections now record the retirement,
and the flow diagram and the "GC free is opt-in" note no longer name the function.

## Tests

Deleted, because both pinned only the retired switch:

- `internal/postmaster/forced_gc_flag_test.go` (`TestForcedGCDisabledByDefaultAndFlagGatesEverything`,
  `TestParseForcedGCEnv`);
- `internal/executor/didwrite_test.go` (`TestContextDidWrite`).

## Inert proof (AGENT.md C6)

- `parseForcedGCEnv("")` was false, so `maybeForceGCAfterCommit` returned at its first statement on every default
  run: no counter bump, no `ReadMemStats`, no GC. Removing its call sites removes one atomic load per commit and
  changes nothing else.
- The variable is set nowhere in scripts, CI, bench configs, git hooks or the Makefile (`git grep`).
- It is not a planner flag (no `flaglabels.go` row), so no plan can depend on it.

## Gates

See the M0147-0015 entry in `.ralph/fix_plan.md`.
