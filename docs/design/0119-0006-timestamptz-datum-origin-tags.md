# `timestamptz` (and `date`) carry their subtype through every door — M0119-0006, 41st slice

Status: accepted
Milestone: M0119-0006 (pg_amcheck server tier — standing slice cluster)
Predecessor: `0119-0006-timestamptz-cast-text-rendering.md` (40th slice)

## 1. The residual this closes

The 40th slice gave `timestamp with time zone` a Datum-level discriminator —
`TimeSubTimestampTZ`, minted by `NewTimestampTZDatum` — and taught the
type-agnostic renderer behind `::text` to dispatch on it. It tagged exactly four
producers: the typed string literal, `evalCast` (including the cross-type
conversion), and the five `prorettype` 1184 functions. It then filed a ledger row
against itself saying the *rest* of the `KindTime` producers had not been
audited, and naming the doors it suspected: binary COPY decode, the btree
index-key decode, the pgoutput decode path, and the array-element renderer.

That row is the resume point this slice takes. The claim being discharged is
narrow and checkable:

> Every decoder that **knows the declared SQL type** must mint the tagged datum,
> so that a value renders the same way regardless of which door it entered
> through.

A door that does *not* have the type in reach cannot be fixed by tagging, and is
explicitly out of scope (§5).

## 2. Why an untagged datum is a wrong answer, not a cosmetic one

`KindTime` is one carrier for five SQL types. The subtype byte is what the
type-agnostic paths — `Datum.Format()`, `CAST`-to-text, string concatenation,
FK-violation `DETAIL` — use to pick an output function. Untagged, a
`timestamptz` renders through `timestamp_out`: the stored UTC wall clock, no
zone marker, session `TimeZone` ignored. Under a non-UTC session that text
denotes a **different instant** than the value, and it disagrees with goopg's own
typed `SELECT` output of the very same column. Nothing errors.

The heap decode (`decodeValuePG`, `internal/executor/codec.go`) already got this
right in the 40th slice. The doors below did not — so the *same column* rendered
one way when read from the heap and another way when read through COPY or
answered from an index key. That is the sibling-path divergence Hard-won Rule #2
exists for, and it is invisible to any test that exercises one path.

## 3. The audit

All 50 non-test `NewTimeDatum(` call sites were enumerated and classified by one
question: **is the declared SQL type in reach at this site?**

| door | site | type in reach | action |
|---|---|---|---|
| binary COPY decode | `copy_binary.go` `copyBinaryToDatum` | yes (`t.Name`) | split the shared `timestamp`/`timestamptz` arm; tag `date` |
| text COPY decode | `copy_text.go` `copyTextToDatum` | yes (`t.Name`) | three-way split of the `timestamp`/`timestamptz`/`date` arm |
| composite index-key decode | `operators_indexonly.go` `decodeIndexKeyColumn` | yes (`typeName`) | split; tag `date` |
| single-column index-key decode | `operators_indexonly.go` `decodeBTreeKeyToDatum` | yes (`typeName`) | split; tag `date` (sibling of the above) |
| `pg_authid.rolvaliduntil` | `pg_authid_sync.go` `buildAuthidUserRow` | yes (compile-time constant) | tag `timestamptz` |
| spill decode | `spill.go` | n/a | already correct — the subtype byte travels with the value |
| heap decode | `codec.go:1252` | yes | already correct (40th slice) |
| encode-direction locals | `codec.go:356/391/420` | — | the datum is consumed by the encoder on the next line and never escapes; tagging changes nothing |
| expression evaluation | `expr.go` (~30 sites) | no | out of scope, §5 |

## 4. The second divergence, which was not in the ledger row

Writing the negative half of the guard surfaced a defect of the same shape one
type over, and larger in blast radius.

`date` has had a **behavioural** subtype since M0097-0063: `TimeSubDate` makes
`Datum.Format()` emit the date-only shape, and `date + integer` (upstream
`date_pli`) dispatches on it. The heap decode sets it. None of the four doors
above did. So a date that arrived by `COPY` — or came back from an index-only
scan — printed `2020-01-01 00:00:00` where the identical date read from the heap
printed `2020-01-01`.

This is the same audit, the same fix and the same rule, found by asking the
negative question ("what must *not* be tagged?") rather than only the positive
one. It is fixed here, in the same arms.

## 5. Deliberately not fixed (each keeps its ledger row)

- **The four target-type-less paths** — `tryParseStringAs`, `EXTRACT`,
  `date_trunc`, and the `pg_authid` validuntil *read* side. They now have a
  discriminator to read, but nothing threads a declared type *into* them. The
  fix is a plumbing change, not a tagging change.
- **The array-element renderer** (`btree_array_key.go`
  `arrayKeyElemRendererPGImage`). It already dispatches on the type *name*, so it
  needs no datum tag; its defect is different — it hardcodes
  `FormatTimestampTZUTC` and ignores the session `TimeZone`. Its own row stands.
- **The pgoutput decode path**, named in the ledger row: no `NewTimeDatum` site
  exists there at HEAD, so the row's suspicion is retracted rather than
  implemented.

## 6. Gates

- `internal/executor/timestamptz_origin_tag_test.go` — new.
  - `TestTimestampTZOriginsCarryTheSubtypeTag`: one instant
    (`2020-01-01 10:00:00+05:30`) decoded through each of the four doors; asserts
    the datum is tagged, the instant survives, and `::text` under
    `TimeZone=Asia/Kolkata` reproduces the literal.
  - `TestTimestampTZOriginsTagSiblingTypesCorrectly`: the negative half —
    `timestamp` and `date` through the same doors must **not** acquire the tz
    tag, and `date` must acquire `TimeSubDate`. Non-vacuous in both directions:
    it fails if the fix widens `isTimestamptzType` into `isTimestampType`, and it
    failed before the fix on the `date` cases.
  - `TestPgAuthidValidUntilIsTimestampTZ`: the non-decoder origin, with an
    assertion on its own premise (the column's declared type).
- Verified red at HEAD before the fix (7 failing sub-cases), green after.
- Package `internal/executor`, the units suite, `scripts/tpch-spotcheck.sh`
  (canonical Q12=2 / Q13=35) and the pre-commit pgbench smoke.
