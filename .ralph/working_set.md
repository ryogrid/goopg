(idle — nothing in flight)

Last loop: M0119-0006 — the checkunique posting-list arm now runs END TO END over
a posting list goopg's own bulk build wrote
(`internal/executor/operators_bt_index_check_posting_test.go`,
`TestBtIndexCheck_CheckUniquePostingListRealTree`). Design §Gates in
`docs/design/0119-0006-checkunique-tier-amcheck.md`.

M-NIGHTLY duty this loop: `ci/logs/action-items.md` is still nightly run
`20260811-014635` (12 items), ALL already filed by loop #87 — nothing new to add.
Eleven remain PARKED per banner; re-run their repros at HEAD before investigating
(TestPort_RegressSuite ran GREEN two commits after that nightly's sha).

What landed + what it FOUND: the three phases are (1) `PageLeafItems` proves the
bulk build really deduplicated, (2) non-checkunique tiers clean while checkunique
raises with `posting 0 and posting 1`, (3) after DELETEing the duplicate rows the
pages are byte-identical yet the tier goes clean — the visibility filter over a
REAL heap. Discovery: goopg's INSERT path never writes a posting list at all
(`dedupConsolidate` only drops exact `(key,tid)` dupes; M0055-0003 Phase B was
left half-done) and the bulk build indexes only live tuples, so NO goopg unique
index can hold a posting list — the test has to flip `catalog.Index.Unique`.
Upstream dedups unique indexes via `_bt_delete_or_dedup_one_page` →
`_bt_dedup_pass` (nbtinsert.c:2778). Ledger row 2026-08-12, resume point
`btree.dedupConsolidate`.

Banner state (re-read this loop): M0130 fully checked; M-NIGHTLY filing done;
banner falls through to M0119 (M0119-0005 blocked on missing hash/gin/gist/spgist/
brin AMs, so M0119-0006 is the actionable head), then M0122.

Next loop: per banner, M0119-0006 again. Remaining named in fix_plan: `box` /
`int4range` key encodings (both types unsupported in goopg entirely —
`encodeBTreeKeyForColumn` raises 0A000; `int4range` at least has initdb `pg_range`
seed rows) and the whole-database unscoped pg_amcheck run. Also open from the
array thread: array SLICES `a[1:2]` (rejected by the LEXER), `interval[]` refused
by `decodeArrayKeyElemText`, TOASTed / multi-dim / NULL-element arrays in logical
decoding, a subscriber round-trip E2E over a publication on an array column.

Gates: build + vet clean; `go test -run TestBtIndexCheck ./internal/executor/`
PASS; `go test ./internal/amcheck/ ./internal/access/btree/` PASS; units
(`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`) PASS; pgbench
smoke via the commit hook. Test-only change (no product code touched), so
tpch-spotcheck / SF0.5 sweep not re-run.

In-flight: none
