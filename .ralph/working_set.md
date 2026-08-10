(idle — nothing in flight)

Last loop: M0119-0006 — the checkunique tier's POSTING-LIST arm is under test
(design `docs/design/0119-0006-checkunique-tier-amcheck.md` §Gates).

M-NIGHTLY duty this loop: `ci/logs/action-items.md` is still nightly run
`20260811-014635` (12 items), ALL already filed by loop #87 — nothing new to
add. Eleven remain PARKED per banner; re-run the regress repros at HEAD before
investigating them (the full `TestPort_RegressSuite` ran GREEN two commits after
that nightly's sha, so they are probably stale).

What landed: `btree.IndexFormat.PGBTPostingRaw` (exported face of the tree's own
`marshalPosting`, sibling of `PGBTItemRaw`) + five fixtures in
`internal/amcheck/verify_nbtree_unique_posting_test.go`. A posting list puts a
uniqueness violation INSIDE one line pointer, which no earlier gate could reach.
The tuple-format case is the load-bearing one: each expanded key carries its own
heap TID, so the duplicate shows only under `CompareKeyAttrs` — the same page
under the bytewise default is asserted to report nothing. Both mutations bite
(collapse the posting expansion; neutralise ` posting N`).

Banner state (re-read this loop): M-NIGHTLY filing done; M0130 fully checked;
banner falls through to M0119 (M0119-0005 is blocked on missing hash/gin/gist/
spgist/brin AMs, so M0119-0006 is the actionable head), then M0122.

Next loop: per banner, M0119-0006 again. Remaining named in fix_plan:
`box`/`int4range` key encodings (both types are unsupported in goopg entirely —
`encodeBTreeKeyForColumn` raises 0A000; `int4range` at least has initdb
`pg_range` seed rows, so a range-type column is the smaller of the two) and the
whole-database unscoped pg_amcheck run. Fresh from this loop: drive goopg's
DEDUPLICATION end to end into a posting list a live `--checkunique` reads
(ledger row 2026-08-11). Older array-thread rows: array SLICES `a[1:2]`
(rejected by the LEXER), `interval[]` refused by `decodeArrayKeyElemText`,
TOASTed arrays + multi-dimensional / NULL-element arrays in logical decoding,
a subscriber round-trip E2E over a publication on an array column.

Gates: build + vet clean; `go test ./internal/amcheck/ ./internal/access/btree/`
PASS; `go test ./internal/executor/ -run TestBtIndexCheck` PASS; units
(`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`) PASS; pgbench
smoke via the commit hook. No planner/executor/codec change (one additive
exported method + tests + docs), so tpch-spotcheck / SF0.5 sweep not re-run.

In-flight: none
