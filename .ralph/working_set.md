(idle — nothing in flight)

All priority milestones through M0128 (M0124/M0125/M0127/M0128/M0123) are
CLOSED. **M0129 (Q74 fix + M0128 verdict follow-ups + residual-ledger
burn-down) is FILED and is the TOP-PRIORITY milestone** (user directive
2026-08-08 — see the Current Priority banner). Milestone doc:
`docs/milestones/0129-q74-fix-and-m0128-followups.md`; implementation plan:
`docs/design/0129-q74-fix-and-m0128-followups.md`.

Next step: **M0129-S1 — Q74 path-selection fix** (attribution done in
M0128-P0.1; resume at `addPathsToJoinrel`, internal/planner/joinpaths.go:139;
subtasks S1.1 diagnose → S1.2 fix → S1.3 pin, per the fix_plan task body).
The standing M-NIGHTLY filing obligation (read `ci/logs/action-items.md`,
file new AI items) still applies each loop. All existing M-NIGHTLY items are
[x] (the 20260808-005620 EPQ/Q95 pair confirmed stale at HEAD); the remaining
unchecked non-M0129 items belong to deferred milestones (M0122, M0119,
M0110, M0095) parked per the banner.

Gates run: make ralph-state-guard OK (auto-repaired status/progress inconsistency)

In-flight: none
