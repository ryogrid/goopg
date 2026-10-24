# M0147-0010 — retire the S30.3 / S32.x diagnostic probes

Status: landed 2026-10-24.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 1 (goopg-private default-OFF
levers, delete). Lever count 34 → 30 (`scripts/lever-count.sh`).

## What was retired

Four environment switches that the M0131 concurrency series added for diagnosis and left in the production binary
(ledger row 2026-08-12, M0131-S32.3).

| switch | file | what it did |
|---|---|---|
| `GOOPG_PAGEIDENT_PROBE` | `internal/storage/pageident_probe.go` | Line-pointer high-water checks and stack dumps when a page's identity disagreed between replay and runtime (M0131-S30.3, docs 0131-0021 / 0131-0024). |
| `GOOPG_S321_PROBE` | `internal/executor/s321_probe.go` | Per-site counters on the UPDATE write and scan phases (M0131-S32.1 / S32.2, doc 0131-0026). |
| `GOOPG_S321_NOWAIT` | same file | Disabled the S32.1 pending-writer wait, for A/B runs. |
| `GOOPG_S322_NOWAIT` | same file | Disabled the S32.2 scan-phase pending-writer wait, for A/B runs. |

## Removed call sites

- `operators_storage.go`:
  - every `s321Note(...)` (21 sites);
  - the `PageIdentityObserve` / `PageIdentityAssertEmit` / `PageIdentityAssertCount` calls in the HOT update path;
  - the probe-only `lpBeforeAdd` line-pointer count. The orphan-cleanup `PageRemoveHeapTuple` stays, now with its
    error explicitly discarded as before.
- `bufpool.go`: the `PageIdentityObserve` calls in the pre-flush and MarkDirty* paths, and the
  `probeAssertSlotIsMapped` duplicate-buffer check with its three calls.
- `smgr.go` and `prefetch.go`: the `PageIdentityObserve` / `PageIdentityExtend` calls on read, write and extend. The
  vectored read keeps its `recordIOTrace` parity with `readBlock`.
- `analysis/pageident_probe.sh`.

**The pending-writer waits stay.** Only the `!s321NoWait &&` / `!s322NoWait &&` conjuncts were deleted, at the three
write-phase and three scan-phase sites (`epqChainPendingWriter` waits and the stale-snapshot retries). Setting NOWAIT
to 1 disabled a correctness fix, so the waits now run unconditionally, as they did by default.

## Inert proof (AGENT.md C6)

- **Gated probes.** At HEAD, every exported probe function returned before doing anything unless its variable was
  `"1"`: `PageIdentityObserve`, `PageIdentityAssertEmit`, `PageIdentityAssertCount`, `PageIdentityExtend`,
  `PageIdentityReportDupSlot` and `probeAssertSlotIsMapped` on `pageIdentProbeEnabled`, and `s321Note` on
  `s321Enabled`. The `lpBeforeAdd` count ran only under `PageIdentityProbeEnabled()`.
- **The NOWAIT conjuncts** were `true` when the variables were unset, so deleting them is an identity.
- **No script sets them.** No script, CI stage or config sets any of the four variables (repository grep: only design
  docs mention them).
- **The executed path is unchanged under the default environment.** Measured instructions are flat: Q23 67.00 →
  67.13 G and Q95 23.47 → 23.44 G.

## Gates

- units PASS;
- spotcheck PASS;
- acceptance arm 24 MATCH;
- sf025 PASS=99 (61 ck) with identical plan shapes;
- census on the tree: TPC-H 22/22, SF0.25 99/99, SF1 99/99, unchanged;
- TestPort\_RegressSuite PASS;
- TestPort\_Isolation PASS.

The sweep's runtime channel showed +531 % from host contention (two unrelated `opencode` processes) with flat
instruction counts. The fire-set gate is out of scope: storage and executor files only (`scripts/fireset-scope.sh`).

## History

Docs 0131-0021, 0131-0024 and 0131-0026 describe these probes as in-tree instrumentation. They stay as the
historical record; this document records the removal.
