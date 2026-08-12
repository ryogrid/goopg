(idle — nothing in flight)

M0131-S20.2a landed and committed: the out-of-line `pg_rewrite.ev_action`
writer. An oversize seeded ev_action now becomes an 18-byte VARTAG_ONDISK
`varatt_external` plus 1996-byte chunks in base/{1,5}/2838, indexed by 2839.

Landed: `internal/initdb/pg_rewrite_toast_writer.go` (new),
`pgBuildIndexTupleOidInt4Key` + `bootstrapToastChunkIndex`
(btree_index_bootstrap.go), `writeMultiPageHeapRowsExternal` (HEAP_HASEXTERNAL),
`bootstrapToastRelationFiles(dataDir, chunks)`, `pgRewriteRowToasted`, and a
`pg_node_tree` decode arm in `internal/executor/codec.go`.
Design `docs/design/0131-0035-pg-rewrite-toast.md` §S20.2a.

Worth carrying (all oracle-measured, PG 18.3 throwaway on a unix socket):
- **The chunked bytes are the compressed varlena MINUS its 4-byte header** —
  chunk 0 opens with `va_tcinfo` (`08 13 01 00 | 00 28 7b 51`). Off-by-four
  here decompresses to garbage.
- `va_rawsize` = UNCOMPRESSED length + 4 in BOTH branches; PG's
  "is it compressed?" test (extsize < rawsize−4) then needs no flag.
- `chunk_id == rule OID + 1`, 0 exceptions over 280 oracle chunk rows.
- Six values are oversize, not eight: `pg_statio_sys_tables` (1756 B) and
  `pg_statio_user_tables` (1759 B) are blocked only by their base view.
- The "detoast on reload" sibling is a DECODE arm: `loadViewsFromHeapForDB`
  discards sub-FirstUserOID rules but decodes every row first, and
  `decodePhysicalPGVarlena` rejects header 0x01 ("external varlena not
  supported") — a startup failure on goopg's own directory.
- A synthetic test payload that repeats one block verbatim compresses ~100:1
  and never crosses the inline budget — vary the payload or the guard tests
  nothing.
- `go test ./internal/testport/` does NOT invalidate its cache on an
  `internal/initdb` change; use an explicit `-count=1` there.

Gates: `internal/initdb` PASS (154 s), `internal/executor` PASS,
`TestE2E_PGColdStartOnGoopgDataDir` PASS (-count=1), 4 break directions proven
fail-when-broken by scripted revert, UNITS PASS, `go build ./...` + `go vet`
clean, pgbench smoke via the commit hook.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all four
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): **S20.2b** — relax
`capture-ev-action.sh` guard #5 to "inline OR toastable", capture the six
oversize views + the two dependents, regenerate via `cmd/gen-nailed-view-tables`,
invert `assertNonCorpusSystemViewIsStillAbsent` onto `pg_policies`, and extend
`assertHostedPGSeesPgRewriteToastRelation` with a real `SELECT * FROM
pg_indexes` — the acceptance S20.2a had no oversize value to run.

In-flight: none.
