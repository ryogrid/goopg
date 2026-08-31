# 0119-0004bi — extended-query vs simple-query type-formatting parity

Status: accepted

## Context

`internal/server/dispatch.go`'s simple-query result loop
(`dispatchSimpleQueryViaExecutor`) and `internal/server/dispatch_extended.go`'s
extended-query result loop (`executeExtendedQueryViaExecutor`) each streamed
result rows through their own independent per-column-type text-formatting
switch on `strings.ToLower(sc.Type.Name)`. The two switches had drifted:
`dispatch.go`'s covered `float4`/`float8`/`char`/`bpchar`/`date`/`time`/
`timetz`/`bytea`/`regclass`, while `dispatch_extended.go`'s only covered
`float4`/`float8` and fell back to `Datum.AppendValueText`'s generic
formatting for everything else.

`Datum.AppendValueText`'s generic `KindTime` fallback
(`internal/executor/datum.go:461-462`) renders the full
`"2006-01-02 15:04:05.000000"` timestamp shape — correct for a raw
timestamp value, but wrong for a `date` or `time` column, which PostgreSQL's
`date_out`/`time_out` render as `"2024-03-04"` / `"13:05:09"` with no time-of-
day or date component respectively. So a client that issues a parameterized
or prepared query (anything driving Parse/Bind/Execute/Sync, not the plain
`Query` message) over a `date`/`time`/`timetz`/`bytea` column got
PostgreSQL-incompatible wire text, even though the exact same query over the
simple-query protocol rendered correctly. This was flagged as a discovery in
the M0119-0004 deferral ledger (loop #61, 2026-07-01) while closing the
built-in `pg_aggregate` regproc-name gap, and tracked as its own follow-up
item in `.ralph/working_set.md`.

Most SQL client libraries (lib/pq included) use the extended protocol only
when the query carries bind parameters, or when a statement is explicitly
`PREPARE`d — a plain no-arg `Query()` call takes the simple-query shortcut.
That is why no prior regression test caught this: every existing wire-level
test exercising `date`/`time`/`bytea`/`regclass` output went through
simple-query.

## Decision

Extract the per-column-type formatting switch out of
`dispatchSimpleQueryViaExecutor`'s inline loop into a shared method,
`(*Server).appendTypedCellText(dst []byte, d executor.Datum, typ
catalog.Type) []byte` (`dispatch.go`), and call it from **both** result
loops. The switch's behavior is unchanged — this is a pure extraction, not a
formatting change — so the simple-query path's output is byte-for-byte
identical before and after. `executeExtendedQueryViaExecutor`'s inline
`float4`/`float8`-only switch is deleted in favor of the shared call, which
gives the extended path the previously-missing `char`/`bpchar`/`date`/
`time`/`timetz`/`bytea`/`regclass` cases for free, and keeps the two
protocols from re-diverging the next time a new type case is added to only
one of them.

`regclass` is a partial fix in practice: a `<col>::regclass` **cast
expression** (e.g. `tableoid::regclass`) is already resolved to a `KindString`
relation name at expression-evaluation time
(`internal/executor/expr.go:597`, the `CastExpr` special-case), so it never
reaches this formatter as an unresolved OID — the wire-level `regclass` case
only matters for a raw `KindInt` OID whose declared column type is
`regclass` without having gone through that cast-eval path (currently no
catalog view exposes such a column; the case is defensive parity with
`dispatch.go`, not exercised by a live code path today).

## Alternatives considered

- **Duplicate the fix in both files.** Rejected — this is exactly how the
  divergence happened in the first place (the simple-query path grew new
  cases over several loops; the extended path was never updated in lockstep,
  per the project's own "sibling paths must change together" lesson).
- **Move the switch to a free function taking a `catalog.Catalog` parameter
  instead of a `*Server` method.** The `regclass` case needs
  `s.cfg.Catalog.(*catalog.InMemory)` for `LookupTableByOID`/
  `LookupIndexByOID`; both call sites already have `s *Server` in scope, so a
  method is the smaller diff.

## Verification

New `internal/server/dispatch_extended_types_test.go`:
`TestExtendedQueryTypedColumnsMatchSimpleQuery` drives
`SELECT tableoid::regclass, '2024-03-04'::date, '13:05:09'::time FROM items
WHERE id = 1` through a raw Parse/Bind/Execute/Sync sequence (forcing the
extended path even with zero bind parameters) and asserts the `date`/`time`
cells render `"2024-03-04"`/`"13:05:09"`, not the `AppendValueText`
`KindTime` fallback's full-timestamp shape. Confirmed failing
pre-fix (`date="2024-03-04 00:00:00.000000"`, `time="1970-01-01
13:05:09.000000"`) by reverting the production diff and re-running with the
test kept; passes post-fix.

Gates: `go build ./...` clean; `go vet ./...` clean; `internal/server` full
suite PASS (including the new test); `internal/executor`+`internal/catalog`+
`internal/planner` suites PASS (unaffected — no change outside
`internal/server`); TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke =
pre-commit hook.

## Deferral

No new ledger row needed for this fix itself (it closes the loop #61 ledger
row's "dispatch_extended.go missing several cases" observation in full for
every type case `dispatch.go` had at the time). The bigger, still-open
sibling item from that same loop #61 row — goopg has no general OID→name
resolution for `regproc`-typed columns at **any** query-output time (neither
protocol resolves `pg_operator.oprcode`/`pg_am.amproc`/etc. to a name) — is
unrelated to this parity fix (it is a missing capability in both protocols
equally, not a divergence between them) and remains tracked in
`.ralph/deferral_ledger.md`.
