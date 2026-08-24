Task just completed: M0134-0153 (float4.sql) — real fix landed (float4send/
float8send), file stays `failed` (6 gaps remain). Committed cb95ff765.

What landed: float4.sql was sized live (`scripts/pg-regress-runner.sh
--verbose float4`): 0/1 PASS, 0% parity. `pg_proc` had already seeded
`float4send`/`float8send` (OIDs 2425/2427) but NO `*send` binary-protocol
function had an `evalFuncCall` dispatch case at all (`grep 'case
"[a-z0-9_]*send"' internal/executor/expr.go` was empty) — every call
42883'd "function float4send does not exist". Added both cases
(`internal/executor/expr.go`, right before `case "get_byte":`), reusing the
already-proven binary-encode logic from `copy_binary.go`'s FORMAT-binary
COPY/wire "float4"/"float8" arms (`pgFloatFromDatum` + `math.Float32bits`/
`Float64bits`, big-endian pack, `NewBytesDatum`). This cleared ~500 diff
lines — every float4send bit-pattern round-trip assertion in the file (the
test's core payload: ~260 hand-picked IEEE-754 bit patterns verifying
float4in/float4out preserve exact bits).

Six independent gaps remain in the residual diff (full file:line citations
+ resume points in `.ralph/deferral_ledger.md` 2026-08-25 M0134-0153):
1. float4in error-message/SQLSTATE parity: overflow inputs need 22003
   `"X" is out of range for type real` + LINE echo (goopg: 22P02 generic
   syntax error, no LINE — ties to standing gap #19, `coerceTextLikeDatum`
   never threads `ExecError.Pos`); leading/trailing whitespace stripped
   before quoting in error text (should be verbatim); malformed NaN/
   Infinity variants silently ACCEPTED instead of erroring.
2. `'nan'::float4 / '0'::float4` wrongly raises division-by-zero (PG:
   nan/0 = NaN, only literal-zero *dividend* triggers the error).
3. Unary `@` (abs) prefix operator on float4/float8 — parser gap, no
   token at all (`SELECT @f.f1` → 42601 "expected expression (got @)").
4. `'9223372036854775807'::float4::int8` should raise 22003 `bigint out
   of range`; goopg silently wraps (Go int64 conversion overflow, no
   PG-faithful range check before the cast).
5. `CREATE CAST (xfloat4 AS float4) WITHOUT FUNCTION` false-positives
   "source data type and target data type are the same" for a
   `CREATE TYPE ... (like = float4)` shell-domain type — same-type check
   compares by storage representation, not OID/name.
6. `bits::integer` (bit(32) literal `x'00000001'`) routes through a
   string-of-digits→decimal parse instead of PG's `bittoint4` bit-pattern
   reinterpretation — no `bit(n)`→`integer` cast dispatch exists.

Each is independently scoped (no shared blocker) but collectively exceeds
one loop — parked per the opr_sanity.sql/oidjoins.sql/password.sql
precedent (land the clearly-scoped win, ledger the rest with concrete
resume points).

CSV: float4.sql rationale updated in place (still `failed`/
`pass_required=no` — file-level pass needs all 6 gaps closed). Regenerated
via `make regen-testport` (needed outer `"..."` wrap for the field since it
contains commas AND embedded `""`-escaped quotes — same lesson as
M0134-0152's row).

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom.
Either (a) pick up one of the 6 float4.sql gaps above (each independently
scoped — #3 unary @ operator or #6 bit→integer cast look like the
smallest/most self-contained), or (b) per task-ID-ascending selection move
to the next unworked M0134 item after -0153 (check fix_plan.md for the
next `[ ]` entry — likely M0134-0154, not yet surveyed this session).

Standing recommendation carried across many loops (unchanged from last
loop):
1. GIN/GiST/SPGiST physical-index plan integration — Seq Scan not
   Index/Index-Only Scan because the AM is catalog-only.
2. btree v0 opclass generality (`internal/executor/operators_ddl.go:15810`
   `isSupportedBTreeKeyType` + `btree_scalar_keys.go`) — confirmed 8+ times.
3. Memoize plan-node type — entirely unimplemented (M0134-0141).
4. Real parallel-worker query execution — recurs across M0134-0008/-0023/
   -0141.
5. Geometry type-system gap — 7 core primitives DONE. Operator-lexer
   family (`<<`/`&<`/`&&`/`&>`/`>>`/`<<|`/`&<|`/`|&>`/`|>>`/`<@`/`@>`/`~=`/
   `<->`/`?-`/`?|`/`?#`/`@@`/`#`) remains open — confirmed again on
   point.sql/polygon.sql residuals.
6. LANGUAGE C dynamic-extension loading gap.
7. Collation-execution-registry gap (5 parked files).
8. BETWEEN-vs-comparison-operator precedence bug (M0134-0113).
9. RAISE INFO/LOG/DEBUG collapse to hardcoded NOTICE wire severity.
10. `::json` cast DETAIL/CONTEXT truncation text (json_errdetail port).
11. pg_shdepend-shaped object-enumeration/CASCADE engine — single most-
    recurring blocker across M0134.
12. `CREATE CONVERSION`-registered procs never consulted by convert_from/to.
13. DDL-event-trigger firing engine + `session_replication_role` GUC.
14. `NonSuperuserRole != ""` "is superuser" convention wrong for
    non-"postgres" superuser roles.
15. inet.sql (M0134-0130) left 11 undispatched scalar functions.
16. pg_init_privs (M0134-0132) is a reconstruction, not a real snapshot.
17. jsonpath's own grammar entirely unimplemented.
18. Full PostgreSQL Large Object facility (M0134-0135) — own milestone.
19. `coerceTextLikeDatum` never threads `ExecError.Pos` — psql LINE echo
    gap, now confirmed on float4.sql too (8th confirmation).
20. `evalCast`'s catch-all pass-through hides real validation gaps for
    box/circle/line `::T` casts.
21. `DropTable` on a PARENT never scrubs `inheritanceChildren`/
    `partitionChildren` (only fixed for the child side, M0134-0140).
22. LATERAL outer-column-ref bug (memoize.sql bonus discovery).
23. No generic system-catalog TOAST-table registration.
24. `money`/`cash` type entirely unimplemented (M0134-0143).
25. CREATE SCHEMA sub-element execution gap — blocks 3+ files.
26. `DROP OWNED BY` has zero parser AST node (blocked on #11).
27. `CREATE PUBLICATION ... FOR TABLES IN SCHEMA` unparsed.
28. pg_proc's provariadic/prosqlbody/proargdefaults still not real.
29. `Routine.ArgTypes` conflates IN-only and ALL-args.
30. `tryHandleRoleDDL` has no wire-protocol notice sink.
31. pg-regress-runner.sh's prerequisite block is not schedule-group-aware.
32. `circle`/`point`/`polygon` scalar CONSTRUCTOR functions unregistered.
33. Polymorphic function type resolution (anyelement/anyarray/
    anycompatible* family) essentially unimplemented for real function
    calls — see M0134-0152 ledger row (5-item breakdown). STRONG
    dedicated-milestone candidate.
34. **NEW (M0134-0153):** No `*send`/`*recv` binary-protocol scalar
    function family was callable at all until this loop added
    float4send/float8send — every other `*send`/`*recv` name (int2send,
    int4send, boolsend, textsend, int4recv, etc.) still 42883s if called
    directly from SQL (as opposed to reached implicitly via FORMAT binary
    COPY/wire, which DOES work per-type in copy_binary.go). Low-priority
    unless another regress file starts exercising them directly.

Gates run this loop: `scripts/pg-regress-runner.sh --verbose float4`
(0% before/after at file level, diff materially shrank — ~500 lines
cleared); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35, 23.3s,
10673MB peak); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS (all packages, including internal/executor 7.1s); `make regen-testport`
PASS (after fixing CSV quoting); `make check-testport-inventory` PASS;
commit-hook pgbench smoke PASS (343/640/11920 TPS across 3 modes);
`make ralph-state-guard`: found+repaired 1 stale-marker inconsistency
(same shape as prior loops — status="running"/progress="completed" from
the previous loop's clean exit), then verified consistent.

In-flight: none.

Note: a concurrent peer session's WIP may still be present in the tree
(.ralphrc, analysis/postgres-oracle-compatibility-report.md,
ci/logs/launch.log, docs/wiki/getting-started.md,
internal/executor/operators_recursive_cte.go, postgres (untracked
convenience symlink), third-party/tpcds-postgres,
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md, .ralph/progress.json) — deliberately left
untouched/uncommitted; only this loop's own files were staged and
committed by explicit pathspec.
