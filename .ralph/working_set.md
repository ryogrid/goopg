Task: M0134-0083 (uuid.sql) — landed a real fix + PARKED 2026-08-23. Committed
(7482d56c). CSV row unchanged (case still `failed`, 3 buckets remain, none flip
it alone).

Files: internal/executor/expr.go (genUUIDv7FromTime + genUUIDv7FromMs refactor),
internal/executor/uuid_v7_time_test.go (new — TestGenUUIDv7FromTimeExtremeYears,
TestGenUUIDv7FromTimeMonotonicAcrossYears), .ralph/fix_plan.md (M0134-0083 PARKED
note), .ralph/deferral_ledger.md (2026-08-23 M0134-0083 row, full bucket detail).

Key symbols: genUUIDv7FromTime/genUUIDv7FromMs (internal/executor/expr.go
~17828) — the uuidv7(interval) call site now builds ms-since-epoch from
ts.Unix()/ts.Nanosecond() instead of the overflow-prone ts.UnixNano() (Go
UnixNano is documented-undefined outside ~1678-2262, int64 ns spans only
~292 years).

Findings: uuid.sql has 3 independent buckets, confirmed via live repro
(throwaway cgroup-capped goopg + a throwaway real PG 18.3 oracle on 15498):
(A) missing LINE n:/^ caret on runtime 22P02 errors — ALREADY-LEDGERED
"goopg never emits FieldPosition" gap (M0127-PS6.2 row, 2026-08-06) — corpus-
wide re-baseline, out of scope here. (B) EXPLAIN drops ::type cast on EVERY
string-literal comparison against a non-text builtin column — confirmed NOT
uuid-specific (same gap for date/inet/macaddr against live PG). Root cause:
resolveExpr's *parser.BinaryOp case (internal/optimizer/planner.go:13466)
only inserts a coercion CastExpr for name/enum/domain comparisons, never for
builtin types — so formatExprQual's CastExpr case (operators_explain.go:1214,
which DOES correctly drop-and-passthrough) never even sees a cast node for
uuid/date/inet/macaddr literals. Fixing needs either extending the planner's
CastExpr insertion (regression risk: PG's cast-display rule for non-literal
casts is context-dependent) or threading column-type context into
formatExprQual directly. (C, blocks the largest diff block — 30-row
monotonicity violation) uuidv7/uuid_extract_timestamp is still wrong for
extreme years — root cause is the PRE-EXISTING, ALREADY-LEDGERED KindTime
nanosecond-carrier overflow (confirmed live: `SELECT now() + interval '236
years'` ALONE, no UUID function, returns 1678-02-01 instead of 2262-02-23) —
same M0119-0006 carrier-range migration ledgered 2026-08-11/12 ("move
Datum.Int for KindTime from nanoseconds to PG's microseconds... needs its
own loop with the full gate battery"). This loop's genUUIDv7FromTime fix is
still correct/needed on its own merits (removes a SECOND independent
overflow layer that would still bite post-carrier-fix) but cannot flip this
bucket alone.

Next step: per banner, next unparked M0134 task by ID ascending =
M0134-0084 (expressions.sql).

Gates run: go build ./... PASS. go test ./internal/executor/... PASS
(includes new TestGenUUIDv7FromTimeExtremeYears/Monotonic). RALPH_PRECOMMIT_
SCOPE=units scripts/ralph-precommit-test.sh PASS (~9 min, full unit suite,
no cache issues). scripts/tpch-spotcheck.sh PASS (Q12=2 rows/22.3s, Q13=35
rows/8.9s — canonical counts). Pre-commit hook pgbench smoke PASS (3 built-
ins, 0 failed txns). make ralph-state-guard: self-repaired one stale
status/progress mismatch from the previous loop's clean exit, then PASS.

Nightly triage: run 20260823-011911 (2 AI items) already filed in fix_plan.md
M-NIGHTLY section (re-verified this loop, no new run posted since) — no
filing action needed.

Delegation: none active.

In-flight: none.
