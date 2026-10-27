# M0147-0034 — retire `GOOPG_SCAN_PREFILTER` (in-scan early qual switch)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 17 → 16 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

E-17/EX3-08 ([design](../executor-ex3-08-scan-resident-qual/DESIGN.md)) absorbs a Filter's qual into `seqScanOp`
and evaluates it once per row, as PG's `ExecScanExtended` does with `Scan.plan.qual`. The qual is evaluated at one of
two positions:
- EARLY, on the deformed column prefix (`planScanPrefilter`);
- LATE, on the finished row.

`GOOPG_SCAN_PREFILTER=off` (default on) made `planScanPrefilter` decline every qual, so everything evaluated LATE.
It was an operational switch for the E-17 measurement arm. No gate, CI config, bench script or env file sets it
(M0147-0030 inventory).

## Removed / changed — the env lever goes, the test seam stays

Unlike the earlier phase-2 levers, this OFF arm is a genuine test oracle. Four tests compare the EARLY position
against the forced-LATE one:
- `TestScanAbsorbedQualValuesMatchFilterOp`: values;
- `TestScanAbsorbedQualErrorPosition`: the error is raised at the late position;
- the IN-list, lead-stage and skip-pass identity tests.

LATE is exactly the position the deleted filterOp occupied, which makes the forced-late run the right reference.

So the operational switch is retired and the seam is kept as an unexported, env-free test knob. This follows the
existing `scanSkipDisabled` (`executor.go`) and the M0147-0017 rule: keep a setter only if tests use it to isolate a
path and no PG-equivalent switch exists. PG has no switch to force a late qual position.

- `scan_prefilter.go`: `scanPrefilterOn`, its `init` env read, `SetScanPrefilterEnabled` (exported, test-only),
  `scanPrefilterEnabled` and the `os` import are removed. The new `scanPrefilterForceLate atomic.Bool` (default
  false) makes `planScanPrefilter` decline when set.
- The four test files switch to `scanPrefilterForceLate`, with the polarity inverted. One comment that named the
  env variable is updated.

## Inert proof (AGENT.md C6)

The env read is gone, and the knob defaults to false, which is the old ON arm. Production behaviour is unchanged.
This executor change is values-checked by the sf025 sweep and the acceptance arm.

## Gates

See the M0147-0034 entry in `.ralph/fix_plan.md`.
