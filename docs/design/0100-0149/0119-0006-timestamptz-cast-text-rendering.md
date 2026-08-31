# `timestamptz` survives the cast to text — and the cast to `timestamp` (M0119-0006, 40th slice)

Status: **accepted** (landed 2026-08-12)
Milestone: M0119-0006 (pg_amcheck server tier / deferral-ledger backlog)
Predecessor: [`0119-0006-timestamptz-output-zone-rendering.md`](0119-0006-timestamptz-output-zone-rendering.md) (39th slice)
Upstream oracle: `postgres/src/backend/utils/adt/timestamp.c`
(`timestamptz_out`, `timestamp2timestamptz`, `timestamptz2timestamp`),
`postgres/src/backend/utils/adt/datetime.c` (`EncodeDateTime`, `DetermineTimeZoneOffset`)

## 1. What was wrong

The 39th slice taught goopg `timestamptz_out`: convert the stored instant into
the session `TimeZone` and print the offset. It could only reach the two output
paths that know the **declared column type** — `dispatch.go`'s
`appendTypedCellText` (SELECT) and `copy_text.go`'s `datumToCopyText` (COPY) —
and closed with a ledger row naming what it could not reach:

> The `::text` cast path still drops the zone: `formatTimeDatumDateStyle`
> receives a bare Datum and `KindTime` cannot tell `timestamptz` from
> `timestamp` — `TimeSubTimestampTZ` is declared but has no producer.

Reproduced at HEAD on a throwaway goopg (5533) against a throwaway PG 18.3:

| expression | goopg @HEAD | PG 18.3 |
|---|---|---|
| `'2020-01-01 10:00:00+05:30'::timestamptz` (SELECT) | `2020-01-01 04:30:00+00` | ✅ same |
| `('2020-01-01 10:00:00+05:30'::timestamptz)::text` | `2020-01-01 04:30:00` | `…+00` |
| same cast, `SET TimeZone='Asia/Kolkata'` | `2020-01-01 10:00:00` ✗ | `2020-01-01 15:30:00+05:30` |

The second row is a cosmetic missing suffix. The third is not: goopg's cast
disagreed with **goopg's own SELECT output of the same value**, and under a
non-UTC session the text denoted a different instant than the one stored, with
no diagnostic. That is precisely the failure mode the 39th slice removed one
path over.

## 2. What the probe found underneath

Tagging the datum made a second, independent defect visible — one that was
already wrong at HEAD and that the ledger had not recorded. goopg treated a cast
**between** the two timestamp types as the identity:

| expression (`TimeZone='Asia/Kolkata'`) | goopg @HEAD | PG 18.3 |
|---|---|---|
| `ts_col::timestamptz` | `2020-01-01 10:00:00` (zone-less) | `2020-01-01 10:00:00+05:30` |
| `tstz_col::timestamp` | `2020-01-01 04:30:00` ✗ | `2020-01-01 10:00:00` |

Upstream these are not the identity. `timestamp2timestamptz` reads the
zone-less wall clock as a **local** time via `DetermineTimeZoneOffset(tm,
session_timezone)`, so the stored instant *moves* by the offset;
`timestamptz2timestamp` renders the instant into the session zone and keeps that
wall clock. goopg returned the datum untouched, which is correct **only while
`TimeZone` is UTC** — the reason it had gone unnoticed, since every goopg
cluster ships UTC. Off by the zone offset in both directions, silently.

## 3. The change

**A discriminator with producers.** `TimeSubTimestampTZ` had been declared since
M0127-P5.9-u with the comment "not yet populated by their producers". It now has
`NewTimestampTZDatum` (`internal/executor/datum.go`) plus `IsTimestampTZ()`,
used at every site that *knows* the SQL type is `timestamptz`:

| producer | file |
|---|---|
| typed string literal (`TIMESTAMPTZ '…'`, `'…'::timestamptz`) | `expr.go` `evalTypedStringLit` |
| cast from text, and the cross-type cast | `expr.go` `evalCast` |
| on-disk decode of a `timestamptz` column | `codec.go` `decodeValuePG` |
| `now`/`current_timestamp`/`transaction_timestamp`/`statement_timestamp`/`clock_timestamp` | `expr.go` (all `prorettype` 1184) |

Sites that hold only a bare instant with no declared type keep `NewTimeDatum`.
Tagging them from a guess is the exact mislabelling the discriminator exists to
prevent — and is why the 39th slice refused to add a suffix.

**One predicate for both halves of the type distinction.**
`tsZoneModeForType` (the INPUT rule: a `timestamptz` applies the text's offset,
a `timestamp` discards it) and the new OUTPUT rule are the same question asked
twice, so both now call `isTimestampTZTypeName` (`copy_text.go`). A producer
that applied the zone on the way in while rendering zone-less on the way out is
the defect this slice fixes; sharing the predicate makes that combination
unreachable.

**The renderer.** `formatTimeDatumDateStyle` (`expr.go`) grows a
`TimeSubTimestampTZ` arm dispatching to `config.FormatTimestampTZ` — the very
function `dispatch.go` and `copy_text.go` call, so the three paths cannot
disagree. It takes a `zone` parameter, resolved by the new `timeZoneFromCtx`
(mirroring `dateStyleFromCtx`); its three callers are the `::text` cast, the
FK-violation DETAIL line (`operators_fk.go`) and `formatDatumDateStyle`.

**The conversion.** Two new ports in `internal/config/timestamptz_out.go`,
`TimestampToTimestampTZ` and `TimestampTZToTimestamp`, reusing the memoised
`sessionLocation`. Go's `time.Date(..., loc)` performs the same local→absolute
resolution `DetermineTimeZoneOffset` does, including the DST-ambiguity
preference for the earlier offset. `evalCast`'s `KindTime` arm calls them and
moves the subtype with the value; ±infinity sentinels, `DATE` and the two
time-of-day subtypes are excluded (their carriers mean something else — see
`NewTimeTZDatum`'s offset in `Datum.Scale`).

## 4. Verification

End-to-end diff of a throwaway goopg (5533) against a throwaway PG 18.3 (5539):
20 probes — stored column / literal / `CAST(...)` / cross-cast, four DateStyles,
four TimeZones, both ±infinity spellings, `now()` self-agreement, and the
`localtimestamp`-stays-plain negative — **byte-identical**.

Gates (`internal/executor/timestamptz_cast_text_test.go`, all mutation-checked):

- `TestTimestampTZCastToTextMatchesPG18Oracle` — 6 PG-18.3 cells; the
  per-DateStyle zone spelling and the three offset widths are what a guess gets
  wrong.
- `TestTimestampTZCastToTextAgreesWithOutputPath` — the sibling guard
  (`pattern_sibling_paths_must_agree`): 4 DateStyles × 4 zones, cast-to-text must
  equal `config.FormatTimestampTZ`, so widening one path alone fails the build.
- `TestPlainTimestampCastToTextStaysZoneless` — the negative half: no zone leaks
  onto plain `timestamp` or `date` under any session zone.
- `TestTimestampTZProducersTagTheSubtype` — the producers, with the
  plain-`timestamp` sibling asserted UNtagged.
- `TestTimestampCrossCastAppliesSessionZone` / `…LeavesInfinityAlone` — §2.

Mutations verified to fail the suite: renderer arm removed; literal producer
reverted to `NewTimeDatum`; cross-cast zone shift removed.

Suite gates: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS,
`TestPort_RegressSuite` PASS (265 s), `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35).

## 5. Deferred (ledger rows filed)

1. **Spring-forward nonexistent local times.** `TimestampToTimestampTZ` inherits
   Go's resolution for a wall clock that does not exist in the session zone;
   upstream `DetermineTimeZoneOffset` has its own `pg_next_dst_boundary` walk.
   The ordinary and fall-back-ambiguous cases agree; the gap case is unasserted.
2. **POSIX `TimeZone` spellings** (`SET TimeZone='+05:30'`, inverted sign) still
   fall back to UTC in `sessionLocation` — inherited from the 39th slice, and now
   reachable from the cast and cross-cast paths too.
3. **Un-audited `timestamptz` producers.** `copy_binary.go`'s binary-COPY
   decode, `btree_scalar_keys.go`'s key decode, the pgoutput decode path and
   `pgarray`'s array-element renderer (which hardcodes UTC — its own 2026-08-12
   row) still mint untagged `KindTime` datums. Values from those origins render
   zone-less through the type-agnostic paths.
4. **The four target-type-less paths** (`tryParseStringAs`, `EXTRACT`,
   `date_trunc`, `pg_authid` validuntil) from the 2026-08-11 row are unchanged;
   this slice gives them a discriminator to read but does not thread the
   declared type into them.
