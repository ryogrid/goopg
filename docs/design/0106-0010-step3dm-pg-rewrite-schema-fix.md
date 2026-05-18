# 0106-0010 Step 3dm Phase A — pg_rewrite TupleDesc fixed to PG18 canonical

## Context

Step 3dl (loop ending 2026-05-18) seeded the first `relkind='v'` view
entry into the bootstrap pg_class + pg_attribute heaps:
`pg_stat_wal_receiver` at OID 12100 with `relhasrules=true`. PG18's
relcache reacts to `relhasrules=true` by issuing a syscache lookup
against `pg_rewrite` (`RewriteRelationId = 2618`) to load the
ON-SELECT `_RETURN` rule that materialises the view's body.

The view definition is unreachable until that pg_rewrite row exists,
and writing a heap tuple into a relation whose nailed TupleDesc does
not match the on-disk layout produces silent decode corruption (PG's
`heap_deform_tuple` walks `attlen`/`attalign` to compute offsets — a
single mismatched column shifts every subsequent column).

Inspection of the in-tree pg_rewrite descriptor exposed a schema drift:

- `internal/initdb/relcache_init.go::pgRewriteAttrs` declared **7
  columns** in the order `(oid, ev_class, ev_type, ev_action, ev_owner,
  ev_enabled, rulename)`.
- PG18's `postgres/src/include/catalog/pg_rewrite.h:32-44` declares
  **8 columns** in the order `(oid, rulename, ev_class, ev_type,
  ev_enabled, is_instead, ev_qual, ev_action)`.
- `ev_owner` does not exist in PG18 (rule ownership has tracked through
  the owning relation's `pg_class.relowner` for many releases).
- `is_instead`, `ev_qual` were missing entirely.
- `ev_action` was typed as `text` (OID 25); PG18 stores it as
  `pg_node_tree` (OID 194, `BKI_FORCE_NOT_NULL`).

The existing index entry for `pg_rewrite_rel_rulename_index`
(`internal/initdb/initdb.go:2403`) already assumed the PG18 canonical
column positions — `entry(2693, 2618, []int16{3, 2}, …)` — so the
index's `indkey` referenced ev_class=column-3 and rulename=column-2
under the prior 7-column layout's slots `ev_type` and `ev_class`.
Without fixing the TupleDesc the index would have indexed the wrong
columns the moment any pg_rewrite tuple was written.

## Change

`internal/initdb/relcache_init.go::pgRewriteAttrs` rewritten to return
the 8-column PG18-canonical descriptor, verbatim from `pg_rewrite.h`:

| attnum | name        | type OID | len | notnull |
|-------:|-------------|---------:|----:|:-------:|
| 1      | oid         |       26 |   4 | yes     |
| 2      | rulename    |       19 |  64 | yes     |
| 3      | ev_class    |       26 |   4 | yes     |
| 4      | ev_type     |       18 |   1 | yes     |
| 5      | ev_enabled  |       18 |   1 | yes     |
| 6      | is_instead  |       16 |   1 | yes     |
| 7      | ev_qual     |      194 |  -1 | yes (BKI_FORCE_NOT_NULL) |
| 8      | ev_action   |      194 |  -1 | yes (BKI_FORCE_NOT_NULL) |

`pg_node_tree` (OID 194) is already known to `codec.go` from M0106-0010
Step 1 (empty-array encoding) which mapped 194 into
`physicalPGTypeAlign` returning `'i'` alignment.

`nailedLocalRels` entry at OID 2618 bumps `relnatts` from 7 → 8 so the
init-file TupleDesc agrees with the heap layout.

## Why this scope only

The natural follow-on — seeding a heap tuple for the view's `_RETURN`
rule — is intentionally deferred to phase B/C. ev_action is a ~5928-
byte `pg_node_tree` serialization of a Query (parsetree containing
RTEs, targetlist, qual); generating it correctly requires either:

  - (i) byte-for-byte extraction from a running PG instance plus
    OID rewrites for the view-side relid (PG-shipped: 12240; goopg:
    12100), or
  - (ii) hand-rolling `nodeToString` over outfuncs.c grammar.

Either path is multi-loop work. The Step 3dl extraction was already
captured in `.ralph/tmp_pg_stat_wal_receiver_ev_action.txt` for the
next loop to operate on. Phase A's only job is to make the destination
TupleDesc safe to write into; without it phase B would have produced
either a corrupted heap or an index referencing the wrong columns.

## Tests

`internal/initdb/pg_rewrite_schema_test.go` (new):

- `TestPgRewriteAttrsMatchesPg18FormPgRewrite` pins all 8
  `(name, TypeOID, Num, Len, NotNull)` tuples against pg_rewrite.h.
- `TestNailedLocalRelsPgRewriteRelnatts8` pins the nailedLocalRels
  entry at OID 2618 to `RelNatts == 8` and `len(Attrs) == 8`.

Verified clean:

- `go test -count=1 -run 'TestPgRewrite|TestNailedLocalRels|TestPgClassRowForView|TestPgStatWalReceiver|TestPgIndex|TestPgClassOidIndex|TestNailedIndexRelnatts' ./internal/initdb/` PASS.
- `go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` PASS.
- Pre-existing `TestSynchronousCommitFlushesByDefault` failure (tracked
  as M0106-0012) reproduced under the unmodified baseline via
  `git stash` round-trip — no new regressions introduced.

## Next blocker (Step 3dm phase B)

Seed a heap tuple in `base/{1,5}/2618` for OID
`pg_stat_wal_receiver._RETURN` with:

  - `oid` = stable goopg-private OID (suggest 12101, adjacent to
    12100; PG normally allocates dynamically at initdb time).
  - `rulename` = `_RETURN`.
  - `ev_class` = 12100.
  - `ev_type` = `'1'` (CMD_SELECT).
  - `ev_enabled` = `'O'` (ALWAYS).
  - `is_instead` = true.
  - `ev_qual` = empty `pg_node_tree` (`<>`).
  - `ev_action` = the 5928-byte `pg_node_tree` extracted via
    `psql -A -t -c "SELECT ev_action FROM pg_rewrite WHERE ev_class =
    'pg_catalog.pg_stat_wal_receiver'::regclass"` against an upstream
    PG instance — but with ev_class-equivalent OIDs (the RTE relid for
    `OLD`/`NEW`) rewritten from PG's dynamic 12240 to goopg's 12100.
    Function OIDs (`pg_stat_get_wal_receiver = 3317`) and type OIDs
    are stable across PG/goopg, so only the view's own OID requires
    rewriting.
  - `pg_rewrite_rel_rulename_index` and `pg_rewrite_oid_index`
    (OID 2692) must be seeded with leaf entries pointing at the new
    heap row.
