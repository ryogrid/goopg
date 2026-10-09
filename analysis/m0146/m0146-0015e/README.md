# M0146-0015e — explicit default-opclass index: restart wrong-rows fix (evidence)

Design: `docs/design/0100-0149/m0146-0015e-explicit-default-opclass-restart.md`.
Repro script: `analysis/m0146/m0146-0015a/opclass-restart-repro.sh`.
Fix-plan: banner item 2a (`- [ ] WRONG RESULTS: an index created with an
explicit opclass returns wrong rows after a clean restart`).

## Root cause

`catalog.Index.ColOpClasses` kept the explicit spelling (`int4_ops`) at
CREATE time while the checkpoint-restart path reverse-resolves the
identical `indclass` OID back to `""`
(`ResolveIndexColumnOpclassName`'s default-equivalence arm — correct for
indexdef rendering). `buildPGIndexKeyDesc` refuses non-empty
`ColOpClasses[i]`, so the format decision flipped across restart: goopg
blob keys at CREATE, PG tuple keys after — probes encoded under a format
the index was never written in → 0 rows.

## Fix

`createBTreeIndex` (operators_ddl.go) normalises an explicit opclass name
that resolves to the column type's own default opclass OID to `""`,
before `bulkBuildBTreeFull` and the WAL/catalog-heap emission — matching
PG, where `indclass` stores only the OID and `pg_get_indexdef` omits a
default opclass. Non-default opclasses (`text_pattern_ops`, user-created)
keep their spelling and stay on the blob format, consistently before and
after restart.

## Evidence

- `repro-after-fix.txt`: `OPC=int4_ops` — `before|49`, `after|49|1`,
  `after-minmax|1|1000` (pre-fix: `before|49`, `after|0|`).
- Control run (`OPC=` empty, `/tmp/nopc.txt`): same correct result,
  unchanged.
- `TestExplicitDefaultOpclassIndexSurvivesCheckpointedRestart`
  (initdb): pin — fails pre-fix at the ColOpClasses normalisation assert
  (`ColOpClasses=[int4_ops] must normalise to ""`); passes post-fix.
  Asserts the probe plans an IndexScan so a seqscan can't mask the
  defect; verifies count=49 before AND after the checkpointed restart.
- Sibling pins unchanged: `TestCreateIndexOpclassAndCollationSurvive-
  CheckpointedRestart` (non-default spellings survive), 
  `TestCreateIndexExtendedPropertiesSurviveRestartViaWAL` (WAL payload).

## Gates

- `RALPH_PRECOMMIT_SCOPE=units` — PASS (44+ packages; initdb 132.9 s).
- `scripts/tpch-spotcheck.sh` — PASS (Q12=2, Q13=33).
- Deferred (ledgered): a pre-fix blob-format explicit-opclass image has no
  on-disk format marker — a post-fix restart still misreads it; REINDEX is
  the repair.
