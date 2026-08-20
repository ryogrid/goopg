# Working set — M0134-0026 PARKED; engine-wide timestamptz session-TimeZone fix shipped

**Task:** M0134-0026 (`guc.sql`) — **PARKED** (case still FAILS).
Design: `docs/design/m0134-0026-timestamptz-literal-session-timezone.md` (indexed).
CSV row unchanged (`failed` -> `failed`), so **no `make regen-testport` this loop**.
Committed `987b0363` and pushed.

**Method note — now ELEVEN loops running.** Interrogating the researcher's first
recommendation changed the work materially again, and this loop it went further:
the researcher REFUTED ITS OWN largest bucket, and the implementer REFUTED ME.
Round 1 recommended a `SET LOCAL` bucket and sized a "~250-line harness bucket"
from inspection alone. Forcing that estimate to be MEASURED collapsed it to -7
lines, and the same round surfaced a strictly more severe defect — silent wrong
data instead of session semantics — which became the shipped fix.

**The lesson that mattered most this loop — an under-configured harness can
report ZERO movement for a demonstrably correct fix.** `guc` measured 767 -> 767
with the default runner, which looks like "the fix did nothing". It is a FALSE
NEGATIVE: `scripts/pg-regress-runner.sh` never exports what real
`pg_regress.c:764-804` sets (`PGTZ`, `PGDATESTYLE`, `PGOPTIONS` intervalstyle,
`LC_MESSAGES=C`), so the case never enters a non-UTC session and the repaired
assertions never execute. With `PGTZ` exported: **760 -> 536 (-224 lines)**.
**Generalise (pairs with M0134-0025's "diff counts can lie about direction"):
before calling a fix ineffective, prove the case actually EXECUTES what it tests.**

**What shipped:** zone-less `timestamptz` input is now read as local wall-clock
in the session `TimeZone` GUC, not anchored to UTC. Single shared root
`parsePGTimestampTextParts` (`internal/executor/copy_text.go`) which all five
input callers reach; reuses the already-correct `misc.TimestampToTimestampTZ`.
PG's DST tie-break ported explicitly into `resolveLocalWallClock`
(`internal/utils/misc/timestamptz_out.go`) — Go's `time.Date` disagrees with PG
on BOTH branches in opposite directions, which corrects a wrong claim in the
M0119-0006 ledger row. Output twin `FormatTimestampTZ` verified already correct
and deliberately UNTOUCHED. PG oracle: `DecodeDateTime`
(`postgres/src/backend/utils/adt/datetime.c:1573-1583`, tie-break :1719-1733).
Recorded honestly: the `codec.go:418` extension I authorised was refuted by the
implementer's experiment (INSERT was already closed transitively via
`coerceRowForConstraintChecks` -> `evalCast`), so it is ledgered as *fixed
defensively, no reachable live SQL caller* — not as closing an inconsistency.

**Five deferral rows appended** (2026-08-20, M0134-0026): `tryParseStringAs` has
no session-zone reach (concrete repro: a `BEFORE INSERT` plpgsql trigger doing
`NEW.tstz := '...'` stores the wrong instant); the `codec.go:418` dead-branch
note; the harness re-baselining task; the M0119-0006 correction; and `guc`'s
three remaining buckets.

**Next step:** select **M0134-0027 (`copy.sql`)** — re-read the fix_plan banner
first (sole ordering authority; its pointer was refreshed this loop). CSV status
is `failed`, so apply the standing rule: re-run
`scripts/pg-regress-runner.sh --verbose copy` at HEAD FIRST and let the result
decide whether the row is stale or gets sized into buckets with exact NET grep
counts. **Consider running it BOTH with and without the `pg_regress` env** — this
loop proved the default harness can hide a real effect. Then interrogate the
park verdict once, as always.

**Gates run:** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS with **Q12=2 / Q13=35** exactly; horology /
timestamptz / timestamp / date / copy / insert each stash-A/B'd vs HEAD — all
byte-identical in size, no case worse; `go build ./...` + `go vet` clean;
guard tests FAIL-pre / PASS-post with oracle-captured values; pre-commit pgbench
smoke PASS (376 / 697 / 12611 tps).

**Delegation:** `tmp/ralph-handoffs/M0134-0026a` (researcher, 3 rounds, DONE),
`M0134-0026b` (implementer, 2 rounds, DONE — report captured by coordinator,
worker tool policy blocked its own write), `M0134-0026c` (tester, 2 rounds, PASS).
**In-flight:** none.
