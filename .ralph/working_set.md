# Working set — M0134-0002 alter_table.sql (C5 btree-inet LANDED)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop landed **C5**
— accept inet/cidr btree keys. The rejection was a hardcoded Go allow-list, not an
opclass lookup: the full btree/inet_ops catalog stack was already seeded.
`isSupportedBTreeKeyType` now accepts `"inet"`/`"cidr"`, plus a new order-preserving
encoder/decoder arm `encodeInetBTreeKey`/`decodeInetBTreeKey` (fixed-width
`[family][masked-network-addr][bits][full-addr]` key reproducing PG
`network_cmp_internal` byte-wise). cidr shares inet_ops via binary coercion; the
expression-key gate routes through the same allow-list.

**Status:** COMPLETE + committed (code `df3ee98b`, bookkeeping `df3be9a4`,
state-guard `9ec00e12`).

**Findings:** diff 4664→4645 (−19), `got "inet"` 1→0. The C5 PKTABLE block
(alter_table.sql:512-513) no longer emits the btree rejection; hunk count unchanged
(84 — the block shares a hunk with the out-of-scope C4 FKTABLE lines). PG oracle:
`network.c:402-420` network_cmp_internal + `inet_net_pton.c` classful cidr mask
(note PG's quirky class-D `/8`→`/4` only for first==224, faithfully ported).

**Files:** `internal/executor/operators_ddl.go` (+`encodeInetBTreeKey`,
`parseInetKeyText`, `parseInetV4Octets`, `cidrDefaultV4Mask`, `maskInetAddr`,
`isInetType`/`isCidrType`, allow-list entry); `internal/executor/btree_scalar_keys.go`
(+`decodeInetBTreeKey`, `formatInetKeyText`); new
`operators_ddl_inet_btree_test.go` (5 tests incl. 32-literal corpus);
`docs/design/0134-0002-alter-table-sql-divergence.md` (C5 row → LANDED);
`.ralph/deferral_ledger.md` (new row); `.ralph/fix_plan.md` (C5 progress note).

**Key symbols:** `encodeInetBTreeKey`/`decodeInetBTreeKey`, `parseInetKeyText`,
`isInetType`/`isCidrType` (operators_ddl.go), `isSupportedBTreeKeyType`,
`decodeScalarBTreeKey` (btree_scalar_keys.go).

**Deferral (recorded):** network_* comparison-operator Go bodies still missing —
btree scans work (byte-wise key compare) but predicate eval `WHERE inet_col = '...'`
+ FK checks on inet/cidr still fail (class C4); no inet binary codec. Ledger row
appended (resume: `network.c` network_cmp/network_lt/…).

**Next step:** C2 (ALTER-TABLE grammar cluster — largest remaining class) needs a
researcher decomposition pass before implementing; then C3/C4/C9/C10/C11
correctness, C6 catalog, C7/C12/C13/C14 formatter.

**Gates run (this loop):** `go test ./internal/executor/ -p 4` PASS (5 new tests);
`go test ./internal/catalog/ ./internal/planner/` PASS; `go build ./...` clean;
`scripts/pg-regress-runner.sh alter_table` diff 4664→4645; pre-commit pgbench smoke
PASS (×3); `make ralph-state-guard` OK (repaired progress.json → in_progress).

**Delegation:** researcher `0134-0002-c5-btree-inet-research` DONE (verdict A);
implementer `0134-0002-c5-btree-inet-impl` DONE (PASS).

**In-flight:** none.
