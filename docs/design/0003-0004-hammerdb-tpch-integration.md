# HammerDB TPC-H Integration (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-28                                             |
| Milestone  | 0003 — HammerDB TPC-H Workload                         |
| Refines    | [root-0006-storage-format.md](root-0006-storage-format.md), [root-0010-parser.md](root-0010-parser.md), [root-0012-executor.md](root-0012-executor.md) |
| Supersedes | —                                                      |

## Problem

HammerDB's TPC-H schema (`HammerDB/src/postgresql/pgolap.tcl`)
defines eight tables — `ORDERS`, `PARTSUPP`, `CUSTOMER`, `PART`,
`SUPPLIER`, `NATION`, `REGION`, `LINEITEM` — with column types
that go beyond what pgbench needed:

- `NUMERIC` (no precision/scale): used for every primary key
  and decimal-valued column. Pgbench used only `INTEGER`.
- `CHAR(N)` / `VARCHAR(N)`: pgbench used `CHAR(22)` for filler
  but never inserted values that exercise the storage codec
  beyond ASCII text.
- `TIMESTAMP`: pgbench's `pgbench_history.mtime` already used
  this; the codec path is shared.

Goopg's parser/analyzer/planner stack handled these type names
end-to-end at the DDL level (the `parseColumnType` accepts any
unquoted name + optional `(N[, N…])` modifier), but the executor
codec rejected `KindInt` datums against any non-{int4, int8,
bool, timestamp} column with `"integer datum cannot encode as
numeric"`. And the lexer didn't recognise decimal or
scientific-notation literals at all — `INSERT … VALUES (… ,
901.01)` failed at parse time with `expected ')' (got .)`.

This loop closes both gaps so HammerDB's `ddl.sql` and a
representative INSERT path run end-to-end against goopg.

## Upstream reference

- `postgres/src/backend/utils/adt/numeric.c` — full upstream
  NUMERIC. v0 stores literals as text strings; arithmetic is
  out of scope until the type system milestone.
- `postgres/src/backend/parser/scan.l` — flex grammar for
  numeric literals (`{decimal}`, `{real}`). Goopg's hand-written
  lexer follows the same shape.

## Decisions

### NUMERIC stored as varlen text, not on-disk numeric

Two extreme choices and a middle ground:

- **Upstream binary**: pack the digit array per
  `numeric.c`'s `NumericData`. Correct for arithmetic but
  requires a real type system before any executor evaluator
  can compute on it.
- **int8 fallback**: encode integer-valued numerics as int8.
  Loses precision for `1.5`, `1e-5`, breaks the loader on
  anything decimal.
- **Varlen text** (chosen): encode the literal byte-for-byte
  via the same varlen frame already used by `varchar`/`char`/
  `text`. Round-trip is lossless for storage and SELECT;
  comparison and arithmetic explicitly out of scope.

The codec change in `internal/executor/codec.go` adds a
dedicated `"numeric"`/`"decimal"` case so:

- `KindInt` datums (e.g. `INSERT INTO t (n) VALUES (1)`)
  flow through `strconv.FormatInt` → varlen text.
- `KindString` datums (decimal literals from the parser)
  store verbatim.
- `KindBytes` is supported for symmetry with `text`.
- DecodeRow emits `KindString` so the wire layer formats
  the value byte-for-byte.

### Decimal/scientific literal lexer

The lexer's digit case grew an optional fractional part
(`.digits`, only consumed when followed by digits — `t.123`
qualified-name form still works) and an optional exponent
(`e[+-]?digits` with conservative back-off when the exponent
body is empty). When either suffix fires, the token is emitted
as the new `TokenNumericLit` with the verbatim source slice as
its value; otherwise it stays `TokenIntLit`.

The AST gains `parser.NumericConst{Value string}`, the analyzer
types it as `numeric`, the planner mirrors the node as
`planner.NumericConst`, and the executor evaluates it as a
`KindString` datum so it lands in the same NUMERIC codec path
as integer literals.

### HammerDB-shape multi-row INSERTs

HammerDB's TPC-H loader (`HammerDB/src/postgresql/pgolap.tcl`)
issues multi-row `INSERT INTO t (cols) VALUES (v1), (v2), …`
with **every value passed as a single-quoted string**, including
into NUMERIC columns:

```
INSERT INTO REGION (R_REGIONKEY, R_NAME, R_COMMENT)
  VALUES ('0','AFRICA','lar deposits');
```

Upstream PostgreSQL accepts this because bare string literals
parse as type `unknown` and are inferred at the assignment site.
goopg types them as `text`, so the analyzer's `isAssignable`
gained a narrow exception: `text → numeric` / `text → decimal` is
allowed because the v0 NUMERIC codec stores string datums
verbatim. `text → int4 / int8` stays a 42804 error — the integer
codec can't accept text bodies, and existing analyzer regression
tests pin this contract.

For TIMESTAMP columns HammerDB calls `to_timestamp(text,
fmt_text)` rather than passing a bare date string. v0 implements
the function in `internal/executor/expr.go` with a tiny format
translator (`pgFormatToGoLayout`) covering the codes the loader
uses (`YYYY`, `Mon`, `MM`, `DD`, plus `HH24`/`MI`/`SS` for
completeness). The translator is intentionally
substring-rewrite, not a real DateStyle parser; locale-aware
month names and timezone-aware parsing wait on the type system.

### Foreign-key syntax accepted, enforcement no-op

HammerDB's TPC-H workflow (`HammerDB/src/postgresql/pgolap.tcl`
lines 529–536) issues eight ALTER TABLE statements after the
data load:

```
ALTER TABLE LINEITEM ADD CONSTRAINT LINEITEM_PARTSUPP_FK
  FOREIGN KEY (L_PARTKEY, L_SUPPKEY)
  REFERENCES PARTSUPP(PS_PARTKEY, PS_SUPPKEY) NOT DEFERRABLE
…
ALTER TABLE LINEITEM ADD CONSTRAINT LINEITEM_ORDER_FK
  FOREIGN KEY (L_ORDERKEY) REFERENCES ORDERS (O_ORDERKEY) DEFERRABLE
```

The parser accepts `ADD [CONSTRAINT name] FOREIGN KEY (cols)
REFERENCES table [(cols)] [NOT DEFERRABLE | DEFERRABLE]` as a
new `AlterTableAddForeignKey` action and stores the local +
referenced column lists plus the deferrable flag on the action.

The executor's `ddlOp` for ALTER TABLE accepts the kind and
performs **no enforcement**: it only verifies that the
referenced relation exists (so dropped tables and typos surface
with SQLSTATE 42P01). Real cascade / lookup enforcement
requires the constraint subsystem and is deferred until at
least the type-system milestone — TPC-H queries don't need
referential integrity to run, only the load to complete
without errors.

The decision matches upstream's "syntactically accepted, but
behavior changes mid-milestone" stance for unimplemented
features (e.g. NOT DEFERRABLE / DEFERRABLE both behave the same
in v0; we record the flag for forward compatibility).

### What's deliberately deferred

- `numeric(p, s)` precision / scale enforcement. The parser
  records the modifier list, but the codec ignores it.
- Cast operator support for NUMERIC (`x::numeric(10,2)`).
  v0's cast is already a no-op for analyzer typing; the path
  works but doesn't enforce.

### Q1–Q22 plan-time coverage

All 22 HammerDB TPC-H query templates parse and plan against the
v0 catalog (verified by `internal/planner.TestPlanTPCHQueriesPlannable`).
The test fixture seeds the eight HammerDB-shaped tables and asks
`planner.Plan` to produce a node tree for each query with parameters
substituted from `sub_query()`'s default range. Planning succeeding
is necessary but not sufficient — execution-time gaps (missing
built-in functions, NUMERIC arithmetic shapes, etc.) surface only
when the executor evaluates expressions against real rows.

Built-in functions added this loop to unblock execution:

- `substr(text, int [, int])` (alias `substring`) for Q22's
  country-code prefix extraction. 1-based indexing, NULL
  propagation, negative-length error matches upstream.
- `to_date(text, fmt)` for Q15's view-defining
  `WHERE l_shipdate >= to_date('1996-01-01', 'YYYY-MM-DD')`
  pattern. Reuses `pgFormatToGoLayout` and truncates to
  midnight UTC.

## Verification

End-to-end smoke against `goopg start -D <dir>` with upstream
`psql` 18.3:

```
CREATE TABLE ORDERS (... O_TOTALPRICE NUMERIC, ...);   -- ×8
INSERT INTO REGION VALUES (0, 'AFRICA', 'lar deposits.');
INSERT INTO PART VALUES (1, ..., 901.01, ...);          -- decimal
INSERT INTO PART VALUES (2, ..., 1.5e3, ...);           -- exponent
SELECT p_retailprice FROM PART;
-- 901.01
-- 1.5e3
```

All eight TPC-H DDL statements complete, INSERTs round-trip,
and `pg_catalog.pg_class` lists the eight tables.

## Out of scope (deferred to subsequent loops)

- HammerDB's full SF1 load against goopg. The shapes
  (multi-row INSERT, string-literal NUMERIC, `to_timestamp`)
  all work end-to-end; running the actual loader requires a
  HammerDB-side TCL harness that's a workstream of its own.
- Real NUMERIC arithmetic / `numeric(p,s)` enforcement.
- Locale-aware / timezone-aware to_timestamp.
- Foreign-key parsing (HammerDB's `ALTER TABLE … ADD
  FOREIGN KEY`). Currently rejected with SQLSTATE 0A000.
- Cost-based planner work (Q1–Q22 join orderings). See
  `docs/milestones/0003-tpch-workload.md` for the scope.

## Cross-references

- Milestone definition:
  [`docs/milestones/0003-tpch-workload.md`](../milestones/0003-tpch-workload.md).
- Upstream HammerDB DDL source:
  `HammerDB/src/postgresql/pgolap.tcl` (lines ~121–138).
- Storage format the codec lives on top of:
  [root-0006-storage-format.md](root-0006-storage-format.md).
