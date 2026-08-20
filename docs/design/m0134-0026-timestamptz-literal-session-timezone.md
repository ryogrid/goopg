# M0134-0026 — A zone-less `timestamptz` literal must be read in the session `TimeZone`

Status: accepted
Task: M0134-0026 (`guc.sql`)
Related: `docs/design/m0134-0025-lateral-outer-colref-aggregate-crash.md` (prior loop)

## Summary

goopg converts a `timestamptz` input string that carries **no** zone
information by parsing the wall-clock digits with a zone-less layout and
letting Go default the location to **UTC**. PostgreSQL instead reads those
digits as *local time in the session's `TimeZone` GUC* and converts that
instant to UTC. Every zone-less `timestamptz` input in a non-UTC session
therefore stores the **wrong instant** in goopg — silently, with no error.

This is not a `guc.sql` bug. It is an engine-wide wrong-value bug that
`guc.sql` merely exposes, because that case is one of the few regress files
that sets a non-UTC `TimeZone` and then inputs a bare timestamp literal.

## Evidence (live, against the PG 18.3 oracle)

Session `TimeZone = America/Los_Angeles` (PDT, UTC-7 on this date):

| | `'2006-08-13 12:34:56'::timestamptz` |
|---|---|
| real PG 18.3 (`expected/guc.out:28-31`) | `2006-08-13 12:34:56-07` |
| goopg at HEAD | `2006-08-13 05:34:56-07` |

Both render with the correct `-07` offset — the **output** path is already
right. The defect is entirely on input: goopg anchored the digits to UTC, so
converting that instant back into the session zone shifts the clock by 7
hours. The stored UTC instant should be `2006-08-13 19:34:56`; goopg stores
`2006-08-13 12:34:56`.

## PostgreSQL's rule (the oracle)

`DecodeDateTime` (`postgres/src/backend/utils/adt/datetime.c:1547-1584`):

- literal carries an explicit numeric offset (`-04`) or an embedded zone
  name → that offset wins; the session `TimeZone` is irrelevant;
- literal carries **no** zone → the comment at `datetime.c:1573-1583` reads
  *"timezone not specified? then use session timezone"*, and
  `DetermineTimeZoneOffset` (`datetime.c:1591-1740`) resolves the wall-clock
  fields against the session zone.

DST edge cases are **defined**, not implementation-detail
(`datetime.c:1719-1733`):

- **spring-forward / non-existent local time** → prefer the *before*
  (standard-time) offset;
- **fall-back / ambiguous local time** → prefer the *after* offset.

Go's `time.Date` resolves ambiguity by its own rule, so the tie-break must be
implemented explicitly rather than inherited.

## What is in scope

One shared root, five callers, none of which pass the session zone today:

| site | path |
|---|---|
| `internal/executor/copy_text.go:1037` `parsePGTimestampTextParts` | **the shared root** — all of the below reach it |
| `internal/executor/expr.go:3208` | explicit `::timestamptz` / `TIMESTAMPTZ '...'` literal cast |
| `tryParseStringAs` → `parseCopyTimestamp` | cross-kind coercion: WHERE comparisons, DDL defaults, plpgsql, btree keys |
| `internal/executor/copy_text.go:474` | `COPY ... FROM` text input |
| extended-protocol bound parameters (text format) | re-enters the literal-cast path |

Fixing the shared root fixes all five, which is why the change is CONTAINED
despite the caller count. `tsZoneMode` already distinguishes `timestamp` from
`timestamptz`, so the plain `timestamp` type — which must keep discarding zone
information — is unaffected by construction.

## Sibling paths (Hard-won Rule #2)

- **Output twin** — `FormatTimestampTZ`
  (`internal/utils/misc/timestamptz_out.go:191-192`) already converts the
  stored UTC instant into the session zone correctly. It needs **no** change,
  and the live evidence above (correct `-07`, wrong clock) is the proof.
- **Already-correct sibling to reuse** — the `timestamp::timestamptz` CAST
  path (`misc.TimestampToTimestampTZ`,
  `internal/utils/misc/timestamptz_out.go:103`) already performs exactly the
  local-wall-clock → UTC conversion this fix needs. The input path should be
  routed through it rather than growing a second, divergent implementation;
  a second implementation is precisely the encode/decode drift this rule
  exists to prevent.

## Design

Thread the session `TimeZone` into the zone-less branch of
`parsePGTimestampTextParts` and resolve the wall-clock fields through the
existing `TimestampToTimestampTZ` conversion, applying PG's documented DST
tie-break. Behaviour is unchanged when the literal carries an explicit offset
or zone name, when the target type is plain `timestamp`, and when the session
zone is UTC — which is why the overwhelming majority of existing tests, which
run at the default UTC, are untouched.

## Blast radius

Exactly one existing test encodes the *old, wrong* behaviour:
`TestTimestampInputDiscardsZoneButTimestamptzKeepsIt`
(`internal/executor/timestamp_zone_discard_input_test.go:47-48`), which
asserts that a zone-less literal is UTC-invariant. Its `timestamp` half stays
valid; its `timestamptz` half asserts the bug and must be re-pinned to the PG
18.3 result. Every other timestamptz/TimeZone test constructs Datums directly
and exercises output only.

Distinguishing "the test encoded the bug" from "the fix caused a regression"
is a judgement the coordinator makes on the evidence, not something the
implementer may assume — any *other* test that flips is a regression signal
and an escalation, not a test to edit.

## Guard test

`TestTimestamptzLiteralAppliesSessionTimeZone` — expected values pinned
against a real PG 18.3 instance (never hand-computed): under
`TimeZone=America/Los_Angeles`, `'2006-08-13 12:34:56'::timestamptz` is the
UTC instant `2006-08-13 19:34:56`. Covers the three branches that must NOT
change (explicit offset, zone name, plain `timestamp`) plus both DST
tie-breaks.

## Deferred

`guc.sql` does not pass with this fix; its remaining buckets are recorded in
`.ralph/deferral_ledger.md` (top-level `SET LOCAL` persisting outside an
explicit transaction with no `WarnNoTransactionBlock` warning — verified live
on BOTH dispatch paths; `ROLLBACK TO SAVEPOINT` not restoring GUCs, which
needs a nesting-level GUC stack and is REFACTOR-tier; and a set of missing
builtins/parser gaps).

Also recorded: the harness observation that
`scripts/pg-regress-runner.sh` does not export the environment real
`pg_regress.c:764-804` sets (`PGTZ`, `PGDATESTYLE`, `PGOPTIONS
intervalstyle`, `LC_MESSAGES=C`). This was **measured, not assumed** — a live
re-run with all of them exported moved `guc` by only 767 → 760 diff lines with
**zero** change in error counts, refuting the initial ~250-line estimate. It is
a real but low-yield harness gap.

## Measured result (post-implementation)

`guc` under the DEFAULT harness: **767 → 767** diff lines, 27 `+ERROR`, 11
`-ERROR` — *zero movement*. That number is a **false negative** and must not be
read as "the fix did nothing".

`guc` with the environment real `pg_regress.c:764-804` exports
(`PGTZ=America/Los_Angeles`, `PGDATESTYLE='Postgres, MDY'`):

| | diff lines | `+ERROR` | `-ERROR` |
|---|---|---|---|
| HEAD (no fix) | 760 | 27 | 11 |
| with this fix | **536** | 27 | 11 |

**−224 diff lines.** The error counts are unchanged because this bug never
raised an error — it silently returned the wrong instant, which is precisely
what makes it dangerous.

The deciding hunk, `expected/guc.out:28-31`:

```
 SELECT '2006-08-13 12:34:56'::timestamptz;
-2006-08-13 05:34:56-07   <- HEAD: literal anchored to UTC, then shown in PDT
+2006-08-13 12:34:56-07   <- with fix: matches PG byte-for-byte
```

The lesson is general and belongs with the M0134-0025 "diff counts can lie
about direction" note: **a regress case measured under an under-configured
harness can report zero movement for a fix that is demonstrably correct.**
Before concluding a fix is ineffective, confirm the case actually executes the
assertions it is supposed to.

## Regression evidence

`horology`, `timestamptz`, `timestamp`, `date`, `copy`, `insert` were each run
with the diff and again at HEAD (stash A/B): all six byte-identical in size, no
case worse. Inside `timestamptz.diff` the `America/Montevideo` 1912 case changed
value from `1911-12-31 20:15:09-03:44:51` to `1912-01-01 00:00:00-03:44:51` —
the intended semantic, still mismatching PG only on an unrelated pre-existing
zone-*abbreviation formatting* gap. Units suite PASS; `tpch-spotcheck` Q12=2 /
Q13=35 exact.

## Correction to this document's own scope claim

The `codec.go:418` extension was authorised on the argument that a plain
`INSERT` would otherwise disagree with an explicit `::timestamptz` cast. That
argument was **refuted by experiment**: reverting `codec.go` alone leaves the
INSERT end-to-end test passing, because `coerceRowForConstraintChecks`
(`operators_storage.go:2303`) already casts every `timestamptz` column through
`evalCast` on INSERT/UPDATE/COPY-VALUES, and literal `VALUES` are typed at build
time by `evalTypedStringLit` — both fixed above. INSERT was therefore already
closed transitively. The `codec.go` change is retained as defensive
consistency, recorded in the ledger as *fixed defensively, no reachable live
SQL caller*, and is covered only by a unit-level test.
