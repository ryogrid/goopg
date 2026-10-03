# M0146-0039b — SELECT … INTO TEMP / UNLOGGED

Status: done (2026-10-03). Parent: M0146-0039 (banner item 2a descendant).

## The defect

goopg's grammar took only `SELECT … INTO [TABLE] name`, a form kept to
match the legacy hand-written parser. So `SELECT 1 z INTO TEMP st` and
`… INTO TEMP TABLE st` were syntax errors, and so were `TEMPORARY`,
`LOCAL TEMP` and `UNLOGGED`. PG accepts all of them.

## The PG behaviour

`into_clause: INTO OptTempTableName` (gram.y). `OptTempTableName` sets
the target's `relpersistence`:

- `TEMPORARY`, `TEMP`, `LOCAL TEMPORARY` and `LOCAL TEMP`, each with an
  optional `TABLE`, give `RELPERSISTENCE_TEMP`;
- `GLOBAL TEMP[ORARY]` gives the same, plus a "GLOBAL is deprecated"
  WARNING;
- `UNLOGGED [TABLE]` gives `RELPERSISTENCE_UNLOGGED`;
- `TABLE name` and a bare `name` give permanent.

The persistence words are unreserved keywords, so `SELECT 1 INTO temp`
still names a table called `temp`.

## What landed

- `into_clause` (grammar/pg_grammar.y) takes `INTO [TEMP | TEMPORARY |
  {LOCAL|GLOBAL} {TEMP|TEMPORARY} | UNLOGGED] [TABLE] name`. GLOBAL is
  taken without PG's deprecation WARNING; the parser has no channel for
  one, and `CREATE GLOBAL TEMP TABLE` (the hand-written ddl.go path) is
  already silent the same way.
- `intoTarget` carries `temporary` / `unlogged`, and `intoWrap` copies
  them onto the `CreateTableStmt` it builds.
- The executor already honours a CTAS statement's persistence
  (M0146-0039a).
- The permanent form is spelled `INTO TABLE name | INTO name`, as gram.y
  spells it. An empty optional `TABLE` in front of the name would have to
  reduce on a `TEMP` / `UNLOGGED` / `LOCAL` lookahead that might still be
  the name, which cost four shift/reduce conflicts. The pin stays at 60.

## Verification

- Live probe against PG 18.3 gives identical relpersistence, namespace and
  values for:
  - `INTO TEMP`, `INTO TEMPORARY TABLE`, `INTO LOCAL TEMP`, `INTO
    UNLOGGED`;
  - `INTO temp`, as a table name;
  - the empty-target-list `SELECT INTO TEMP s6 FROM …`.
- No temp table survives into a new session.
- `TestSelectInto` now pins:
  - every form with `assertParity`, plus a field check on
    Temporary/Unlogged;
  - `temp`, `unlogged`, `local`, `global` and `temp.x` as names;
  - `INTO GLOBAL TEMP[ORARY]`, and `INTO GLOBAL name` still rejected as in
    PG.
- The golden diff is one line flipped from reject to accept, plus 13 new
  pins.
- Regress A/B against HEAD: copyselect, create_table, horology,
  numerology, stats, temp, without_overlaps and select_into are
  byte-identical. copyselect's `COPY (SELECT … INTO TEMP …)` still raises
  `COPY (SELECT INTO) is not supported`.
- Gates: units, spotcheck, SF0.25 sweep 96/96, TPC-H arm.

## Residuals (ledgered)

- PG's "GLOBAL is deprecated in temporary table creation" WARNING is not
  raised for `INTO GLOBAL TEMP` or for `CREATE GLOBAL TEMP TABLE`. The
  parser has no WARNING channel; the divergence is recorded in the goyacc
  playbook §12.5.
