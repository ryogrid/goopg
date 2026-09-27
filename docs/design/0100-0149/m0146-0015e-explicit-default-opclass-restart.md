# M0146-0015e: explicit default-opclass index survives a clean restart

Status: **LANDED**. Task: `.ralph/fix_plan.md` banner item 2a — "an index
created with an explicit opclass returns wrong rows after a clean restart"
(found 2026-09-25 by M0146-0015a). Repro:
`analysis/m0146/m0146-0015a/opclass-restart-repro.sh`. Evidence:
`analysis/m0146/m0146-0015e/`.

## Defect

```sql
CREATE TABLE t1 (a int4);
INSERT INTO t1 SELECT g FROM generate_series(1,1000) g;
CREATE INDEX t1_a ON t1 USING btree (a int4_ops);
SELECT count(*) FROM t1 WHERE a < 50;   -- 49
-- clean stop; start
SELECT count(*) FROM t1 WHERE a < 50;   -- 0 (pre-fix); seqscan min/max fine
```

`int4_ops` IS int4's default opclass, so upstream's `pg_index.indclass`
stores the same OID whether the user spelled it or not. goopg kept the
*spelling* though: the live `catalog.Index.ColOpClasses[0] == "int4_ops"`,
while the checkpoint-restart path (`loadUserIndexesFromHeap` →
`ResolveIndexColumnOpclassName`, `internal/catalog/catalog.go`)
deliberately reverse-resolves a default-equivalent OID back to `""` — the
right answer for indexdef rendering.

`buildPGIndexKeyDesc` (`internal/executor/pgindex_keydesc.go`) refuses any
non-empty `ColOpClasses[i]` — explicit opclasses stay on goopg's
order-preserving **blob** key format, indexes it can describe get PG's
per-datum **tuple** key format (M0130-S11.4). The spelling asymmetry
therefore flipped the on-disk-format decision across a clean restart:
blob at CREATE, tuple after. Probes encoded keys under a format the index
was never written in and silently returned no rows — the S2-class wrong-
rows defect.

## Change

One site: `createBTreeIndex` (`internal/executor/operators_ddl.go`), after
`idx.ColOpClasses = xp.ColOpClasses` and BEFORE `bulkBuildBTreeFull` and
the WAL/catalog-heap emission, normalises each explicit opclass name that
resolves to the column type's own default opclass OID
(`ResolveIndexColumnOpclassOID(name, typeName, methodOID) ==
ResolveIndexColumnOpclassOID("", typeName, methodOID)`, both non-zero) to
`""`. Both construction paths — live DDL and heap reload — then produce
the identical catalog entry, and `pg_index.indclass` is unchanged because
the explicit name resolved to that same OID anyway.

This is PG's own information model: `indclass` carries only the OID and
`pg_get_indexdef` does not print a default opclass, so "explicitly spelled
default" is unrepresentable downstream of CREATE — the only self-
consistent interpretation is to collapse it to the default, which the
tuple format then honours on both sides of a restart.

Consistent cases:

| declaration | indclass OID | ColOpClasses (live) | ColOpClasses (reload) | format |
|---|---|---|---|---|
| `(a)` | default | `""` | `""` | tuple |
| `(a int4_ops)` — new | default | `""` (normalised) | `""` | tuple |
| `(a int4_ops)` — pre-fix | default | `int4_ops` | `""` | blob→tuple **(broken)** |
| `(b text_pattern_ops)` | non-default | kept | kept | blob |
| user opclass | user OID | kept | kept | blob |

WAL replay is consistent on both generations: post-fix payloads carry the
already-normalised `""`; pre-fix payloads keep the spelling, refuse the
tuple format, and so continue to read their pre-fix blob image.

## Residual (ledgered)

An explicit-opclass index created **before** this fix carries a
blob-format image with nothing on disk recording which of the two key
formats it is — the `pg_index.indclass` OID cannot distinguish it from an
implicit index. A post-fix restart still reconstructs it as tuple-format
and misreads it; the repair is `REINDEX` after upgrade. A durable
per-index format marker (e.g. a reloption or pg_index side channel) is the
only automatic fix and is deferred — no PG-shaped field exists for it.

## Tests

- `TestExplicitDefaultOpclassIndexSurvivesCheckpointedRestart`
  (`internal/initdb/index_ddl_recovery_test.go`): creates the
  `int4_ops` index over 1000 rows, pins the normalised catalog entry,
  asserts the `a < 50` probe plans an `IndexScan` (a seqscan would mask
  the defect), verifies `count(*) = 49` before AND after a graceful
  checkpointed restart. Fails pre-fix at the normalisation pin.
- `TestCreateIndexOpclassAndCollationSurviveCheckpointedRestart`
  (pre-existing): non-default `text_pattern_ops`/`varchar_pattern_ops`
  keep their spellings across restart — unchanged.
- `TestCreateIndexExtendedPropertiesSurviveRestartViaWAL`: WAL-payload
  opclass round-trip — unchanged.

## Gates

- `RALPH_PRECOMMIT_SCOPE=units` — 44+ packages PASS (incl.
  `internal/initdb` 132.9 s, `internal/executor` 19.1 s).
- `scripts/tpch-spotcheck.sh` — Q12=2, Q13=33, PASS.
- Live repro `opclass-restart-repro.sh`: `before|49`, `after|49|1`,
  `after-minmax|1|1000` (was `after|0|`).
