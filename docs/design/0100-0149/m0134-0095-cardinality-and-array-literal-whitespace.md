# M0134-0095 — `brin.sql`: `cardinality()` dispatch + `parseTextArray` comma-space bug

**Status:** PARKED (`failed`) — sized live against the PG 18.3 oracle
(`scripts/pg-regress-runner.sh --verbose brin`). Diff 246 → 245 lines (the
metric is near-flat because a large brin-specific blocker set dominates the
file; the two fixes below are real and independently verified).

## What `brin.sql` needs, in full

`brin.sql` exercises real BRIN index infrastructure end-to-end: the
`brin_summarize_range`/`brin_desummarize_range`/`brin_summarize_new_values`
maintenance functions, the planner actually choosing a Bitmap Index Scan over
a BRIN index based on column correlation statistics, `inet`/`cidr` typed
literals used as the LHS of arithmetic (`inet '10.2.3.4' + tenthous`),
`numrange()`, `tid` literal comparison inside a PL/pgSQL `EXIT WHEN`, and a
`brinopers` fixture table gated by a two-column `CHECK (cardinality(op) =
cardinality(value))` constraint. Each of the first five is an independently
large, unimplemented subsystem (see the deferral ledger row for the sizing).
This slice made a CONTAINED pass at the sixth: the `cardinality()` gap that
was the file's very first hard error.

## Bug 1 — `cardinality(anyarray)` had no dispatch arm

`cardinality` had a `pg_proc` entry (OID 3179,
`internal/catalog/pg_proc_names_generated.go`) and was even listed in
`isKnownBuiltinFunction` (`internal/executor/operators_call.go:749`), but
`evalFuncCall` (`internal/executor/expr.go`) had no `case "cardinality":` arm
at all — every call raised `42883 function cardinality does not exist`. This
is the exact "seeded in pg_proc but never wired to evalFuncCall" pattern
already named in the M0134-0090 (`amutils.sql`) ledger row for a different
function family.

Fixed by adding a `case "cardinality":` next to the existing
`array_length`/`array_upper`/`array_lower` arms
(`postgres/src/backend/utils/adt/array_userfuncs.c` `array_cardinality` →
`ArrayGetNItems`): unlike `array_length`, `cardinality` takes no dimension
argument and sums every element (goopg arrays are 1-D only, so this is just
the element count), returns `0` — not NULL — for an empty array, and NULL
only for a NULL array argument.

## Bug 2 — `parseTextArray` mis-split quoted elements after `", "`

Landing bug 1 alone still failed `brinopers`'s CHECK constraint on a row
whose arrays were legitimately equal-length:
```
'{>, >=, =, <=, <}'                                            -- 5 elems
'{"(0,0)", "(0,0)", "(8800,0)", "(9999,19)", "(9999,19)"}'      -- 5 elems
'{100, 100, 1, 100, 100}'                                       -- 5 elems
```
`parseTextArray` (`internal/executor/expr.go`) is the shared element-splitter
behind `cardinality`/`array_length`/`array_upper`/`array_lower`/`array_remove`
and others. Its unquoted-element branch read from the current cursor to the
next comma with no leading-whitespace skip. After consuming a comma, the next
element in a `", "`-separated literal starts with a space, not directly with
`"` — so the cursor landed on the space, took the *unquoted* branch (since
`inner[i] != '"'`), and read `" \"(0"` (space, quote, digits) as one bogus
element before hitting the internal comma inside `(0,0)`. A 5-element tid
array mis-counted as 9, so `cardinality(value) != cardinality(op)` even
though the actual literal was well-formed. Real PG's `ReadArrayStr`
(`postgres/src/backend/utils/adt/arrayfuncs.c`) skips leading whitespace
before every element for exactly this reason.

Fixed by skipping ASCII space characters at the top of the element loop,
before deciding whether the element is quoted. This is a genuinely
high-blast-radius shared helper (per the project's "sibling paths must stay
in sync" caution) — every one of its callers benefits from the fix, not just
`cardinality`.

## Verified

`internal/executor/cardinality_test.go`
(`TestEvalCardinality`): element counting, empty-array-is-zero-not-NULL,
NULL propagation, and the exact comma-space quoted-element regression that
motivated the `parseTextArray` fix. `go build ./...` clean;
`TestEvalArrayRemove`/`TestEvalArrayRemoveNested` (parseTextArray's other
consumer) re-run and still pass.

## What's still missing (PARKED, see deferral ledger)

After both fixes, the file surfaced a THIRD, unrelated bug on the very next
statement (a different `INSERT ... box[]` row raising `NULL array elements
are not supported`, in the column-coercion path for a `text[]` target column
— not `parseTextArray`, not `cardinality`) plus the five large subsystems
named above (`inet`/`cidr` literal arithmetic, `numrange`, `tid` literal
comparison in PL/pgSQL, and the BRIN summarize/desummarize functions plus
real planner BRIN usage). None is reachable inside this slice. Resume points
are recorded in the `.ralph/deferral_ledger.md` row for M0134-0095.
