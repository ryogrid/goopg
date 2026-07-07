Task: M0122-0018 — `isfinite()` NULL propagation bug (found during prior
loop's M0122-0017 survey, not itself an unimplemented_feat.json backlog
entry). COMPLETE and committed this loop.

Files: internal/executor/expr.go (evalIsFinite: check d.IsNull() before
computing result, return NullDatum instead of NewBoolDatum(!d.IsNull())),
internal/executor/isfinite_test.go (new, TestIsFiniteNullPropagates),
docs/design/0003-0006-date-interval-arithmetic.md (2026-07-08 follow-up
section — same doc that already covers the sibling M0097-0004
justify_hours/justify_days/justify_interval fix, isfinite belongs to the
same date/interval cluster), docs/design/README.md (0003-0006 row appended
in place), .ralph/fix_plan.md (M0122-0018 added as [x]).

Key symbols: evalIsFinite (internal/executor/expr.go, right before the
evalJustifyInterval comment block — note: the doc comment for evalIsFinite
sits ~85 lines ABOVE the function itself, orphaned by a prior refactor that
moved the function down without moving its comment; pre-existing oddity,
left alone this loop since not in scope).

Findings: isfinite has no NotStrict marker on any of its 4 wired
pg_proc_seed_data.go OIDs (1373 date/1389 timestamp/1390 interval/2048
timestamptz) — internal/initdb/pg_proc_seed_data.go — so per the strict-
function convention (goopg has no generic strictness pre-check in
evalFuncCall's giant switch; every case handles NULL manually) isfinite must
propagate NULL, not evaluate. Confirmed non-vacuous via git stash on
expr.go alone (4 NULL cases fail: "IsNull() = false, want true").
`unimplemented_feat.json` was NOT touched — this bug was never a backlog
entry (isfinite's OID coverage itself was already correctly scoped by
M0097-0004; only the NULL-handling omission was new).

Next step: pick the next task. Two concrete quick-win candidates remain
unsurveyed in depth: (1) m0097-0009 pg_get_serial_sequence() convention-
guessed name, (2) M0087 loadTables no-op for non-InMemory catalogs.
Alternatively, per the Current Priority banner, scope M0119-0004's
per-database catalog-isolation gap or M0119-0006's 005_opclass_damage as a
proper multi-loop milestone item (needs its own design doc + explicit
multi-loop scope acknowledgment first, per the M0119 per-task rule) — still
not attempted. M-NIGHTLY: ci/logs/action-items.md's 8 AI- items (run
20260707-000712) are all already triaged into fix_plan.md M-NIGHTLY tasks
(7 checked [x] resolved, 1 — pgbench/nightly btree keyLen mismatch — checked
[x] but its own text still describes an unresolved multi-day buffer-pool
investigation; re-read that item's full text before assuming it's actually
closed if picking it up).

Gates run: go build ./... clean. go test ./internal/executor/... PASS
(full package, 3.5s). scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33).
RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh PASS
(0 failed transactions, all 3 workloads). make ralph-state-guard PASS
(auto-repaired 1 benign issue, identical pattern to every prior loop).

In-flight: none (no background agents or long-running processes left
running).
