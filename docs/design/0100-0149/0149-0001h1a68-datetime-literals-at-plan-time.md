# M0149\-0001h1a68 — date/time literals are read when the statement is planned

## Problem

A timestamptz literal was read when it was evaluated, under the TimeZone in force at that moment. A prepared
statement therefore followed a later `SET TIME ZONE`, and EXPLAIN printed the literal as written
\(`'2020-01-01'::timestamp with time zone`\) where PG prints the converted value
\(`'2020-01-01 00:00:00-05'::timestamp with time zone`\).

## What PG does

Parse analysis coerces a literal to its type with the type's input function, which reads TimeZone and
DateStyle, and stores the result as a Const. The value is fixed from then on, and EXPLAIN prints it through the
output function.

## Design

- `PlannerSettings` carries `TimeZone` and `DateStyle`. They are read from the session and are part of the
  plan cache key, so a cached plan is never reused under another zone or style.
- After planning, `canonicalizeDateTimeLiterals` walks every expression of the plan, sublinks included. A typed
  literal of type date, time, timetz, timestamp or timestamptz, a cast of a string to one of them, and a string
  compared with an operand of one of them get the text the output function prints.
- The executor hook `canonicalDateTimeLiteral` reads the value under the session settings, prints it, and keeps
  the text only when it reads back as the same value. Otherwise it tries the ISO form, then leaves the literal
  as written.
- Literals naming the current time \(`now`, `today`, `tomorrow`, `yesterday`\) are left to run time. PG converts
  them at parse time too, but goopg reuses a cached plan across statements where PG parses again.
- PREPARE records the TimeZone and DateStyle in force; EXECUTE plans with them \(`Context.PreparedLiterals`\).

Reading back `'Sat Apr 02 12:00:00 2005 CST'` under horology's POSIX zone `CST7CDT,M4.1.0,M10.5.0` looped
without end in `zonePeriods` \(`internal/utils/adt/datetime/decode.go`\), because the zone built from a POSIX
string reports a bound that does not move forward. The loop now stops there.

## Current\-time literals \(M0149\-0001h1a81\)

- **Session zone.** `today`, `tomorrow` and `yesterday` are midnight of the current date in the session TimeZone,
  and `now` read as a timestamp without time zone is the local wall clock \(DecodeDateTime, datetime.c\).
  - parseTimestampSpecialLiteral and parseDateSpecialLiteral take the zone, and convert with
    misc.TimestampTZToTimestamp and TimestampToTimestampTZ. They used the UTC date, which is a day off near
    midnight.
  - `current_date` reads the local date as well.
- **Run time, not plan time.** The literals are still evaluated when the statement runs, because goopg's plan cache
  reuses a plan across statements where PG parses again.
  - The canonicalizer types a current\-time string compared with a date/time operand as that operand's type, as a
    TypedStringLit holding the original text, so EXPLAIN shows `'...'::date`.
  - EXPLAIN \(text format\) prints each such literal as the instant it denotes under the statement's clock.
    `optimizer.CurrentTimeLiterals` finds the literals, `currentTimeLiteralTexts` reads them, and
    `subPlanReg.currentTimeText` substitutes the text at render time, so a cached plan is never changed.
- `allballs` is a constant \(00:00:00\) and is canonicalized like any other literal.
- `t > 'epoch'` is already an index condition.
- **Not done:**
  - A prepared statement re\-reads `now`, where PG fixes it at PREPARE.
  - Within a transaction, `now` is the statement time and not the transaction start.
  - JSON, XML and YAML EXPLAIN still print the literal as written.
  - `timestamptz::date` and `date_trunc` ignore the session zone; this is appended to the S2 M0149\-0001h1a75.

## Literals in views \(M0149\-0001h1a81a\)

PG converts a view's literals during parse analysis of CREATE VIEW, so the rule keeps the instant read under the
creating session's settings. goopg stores the view's source text and re\-plans it at each use, so
`bindViewDateTimeLiterals` \(internal/executor/view\_datetime\_literals.go\) fixes the literals in the text at
creation:

- The body is re\-parsed on its own, so positions are relative to the text, and planned with the session's
  TimeZone and a DateStyle of `ISO, <session order>`.
- For each datetime TypedStringLit, and each string cast to a datetime type, whose planned text differs from the
  quoted literal at its position, the quoted span is replaced. A type keyword before the quote is kept.
- Escape, Unicode, bit and dollar\-quoted strings are left alone.
- The new text is parsed again and stored, both as the query and as the view text, so the binding is durable.
- A timestamptz literal carries its offset and an ISO date is unambiguous, so the stored text reads back as the
  same value under any session settings.
- Not done:
  - Current\-time literals \(`'now'`\) in a view are still read at each use, where PG freezes them at CREATE VIEW.
  - Materialized views are not rewritten.
  - pg\_get\_viewdef shows the bound text, not PG's deparse.
