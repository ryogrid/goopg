(idle — nothing in flight)

M0131-S21g LANDED (loop #170) — **the S28 reverse crash E2E is GREEN**. A real PG
18.3 cluster SIGKILLed mid-life is now served by goopg end to end: all twelve
row-equality checks match PG's own pre-crash answers, and the `FOR UPDATE` row is
readable and updatable afterwards.

Files: `internal/wal/recovery.go` (new `decodeXLogHeapUpdateNewTuple`,
`replayDecodedXLogHeapUpdate` now keeps the flags byte and builds the tuple inside
the BLK_NEEDS_REDO branch), `internal/wal/pg_assembled_emit.go` (the two flag
constants), `internal/wal/heap_update_pg_test.go` (3 sub-tests), design
`docs/design/0131-0015-*` §"S21g" + Guard 10, fix_plan (S21g checked, S21e closed),
3 ledger rows.

The discovery: **S21e's diagnosis was wrong.** It is not cold-start catalog
visibility. The boot reload scanned the replayed `base/5/1259` and kept 424 of 429
pg_class tuples; the three it could not decode (`physical row too short: len=4`)
were exactly `s28_items`/`s28_sub`/`s28_scratch`, whose index and toast rows
reloaded fine. Replay wrote the husks: `log_heap_update` omits the leading/trailing
run the new version shares with the old one (`XLH_UPDATE_PREFIX_FROM_OLD` 0x20 /
`SUFFIX_FROM_OLD` 0x40, lengths logged in front of the `xl_heap_header`), and goopg
discarded the flags byte and wrote the record's MIDDLE bytes as the whole tuple. A
`pg_class.relhasindex` flip compresses to ~4 bytes — which is why tables were hit
and their indexes were not.

Worth carrying: this one was SILENT. Every other S21 slice announced itself by
refusing to start; this produced a structurally valid page holding a corrupt tuple,
and only a decoder downstream noticed.

Gates: `internal/wal` + `internal/storage` + `internal/initdb` PASS, `-race` on the
touched wal tests PASS, UNITS precommit PASS (warm cache), **S28 E2E PASS** (was
red for 5 loops), S3 cold-start E2E still PASS, pgbench smoke via the commit hook,
`make ralph-state-guard` OK. Fail-when-broken proven by re-inserting the
"treat it as uncompressed" path → all 3 sub-tests fail.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4 `## AI-`
items already filed under M-NIGHTLY, nothing new.

Next loop (banner = M-NIGHTLY filing, then M0131): S21f (mechanical, 3 redo paths
that refuse where upstream skips) or S23 (the cheap LogicalMessage/Generic/CommitTs
tail). Both ledger rows from this loop (new_xmax, ALL_VISIBLE_CLEARED) are small
enough to fold into whichever is picked.

In-flight: none.
