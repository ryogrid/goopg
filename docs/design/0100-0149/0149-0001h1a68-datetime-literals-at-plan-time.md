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

## Not covered \(M0149\-0001h1a81\)

- Views keep their source text and are planned again at each use, so their literals follow the zone of the query.
- `now` and the other current\-time literals stay unconverted, and EXPLAIN prints them as written.
- `t > 'epoch'` is not used as an index condition, as PG does.
