# 0119-0006bq — amcheck regressions: `postgres`-db relation scope and tuple-format key comparator

- Milestone / task: M0119-0006 (pg_amcheck server tier), regression-fix slice
- Status: **accepted**
- Date: 2026-09-19
- Related: `0119-0006-verify-heapam-per-db-oid-resolution.md` (bo — the commit
  this slice partially corrects), `0119-0006-opclass-comparator-dispatch-amcheck.md`,
  `fea5e8dd4` (M0130-S11.4 B2-c, the tuple-key flip), deferral-ledger row
  2026-09-02 (whole-database pg_amcheck on non-default DBs)

## Problem

`go test -run 'TestPort_PgAmcheck' ./internal/testport/` shows the whole
amcheck server tier red at HEAD: every ported test that exercises a *healthy*
fixture now skips because the baseline is not clean. Two independent
regressions, each landed after the tests went green.

### Regression A — `verify_heapam` cannot open any relation in `postgres`

`372f9ded8` (slice bo) threaded `ctx.CurrentDatabaseOid` into
`verifyHeapamResolveTable` so OID/name lookups pin the caller's database
namespace. That fixed `pg_amcheck --schema=s1 amcheckdb` but broke the default
database itself: `postgres`'s `pg_database` OID is 16384
(`catalog.PostgresDBOid`), while `catalog.NamespaceDBOid` deliberately aliases
`PostgresDBOid` (and 0) onto `DefaultDBOid` — user tables created while
connected to `postgres` live in namespace **1**, not 16384. Passing the raw
connection OID searches an empty namespace and every relation reports 42P01
`could not open relation: relation does not exist`. The slice's own gates
(units + tpch-spotcheck) never ran the ported amcheck tests, so it escaped.

Repro (fresh cluster, db `postgres`):
`CREATE TABLE t(a int); SELECT * FROM verify_heapam('t'::regclass)` → 42P01.

The mirror-image latent bug is in the same file's sibling:
`btIndexResolve` (`operators_bt_index_check.go`) passes **no** dbOid at all, so
it silently resolves against `DefaultDBOid` — fine for `postgres`, broken for
any genuinely non-default database's indexes. bo fixed one side, missed the
other; this slice does both with the same mechanism.

Fix: both resolvers translate the connection OID through
`catalog.NamespaceDBOid` — inside `verifyHeapamResolveTable` (covers the three
shared call sites: `verify_heapam`, `pg_get_sequence_data`,
`pg_sequence_parameters`) and as a new `dbOid` argument on `btIndexResolve`
supplied from `evalBtIndexCheck`. This matches every DDL path, which already
calls `catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)`.

### Regression B — `bt_index_check` flags every multi-page healthy index

`fea5e8dd4` (the S11.4 B2-c flip, `pgIndexTupleKeys = true`) changed what an
on-page key IS: under the tuple format `indexFormat.parse` returns the whole
`IndexTupleData` (header incl. `t_tid`, key attributes, heap-TID tiebreak), not
the bare order-preserving blob. All three amcheck comparator seams still
default a nil `KeyComparator` to `nbtree.CompareKeys` — a *bytewise* compare
that now orders by tuple header bytes (heap TID first) before ever reaching the
key. A healthy 500-row index reports `high key invariant violated` on its
leftmost leaf and `item order invariant violated` elsewhere; even
`bt_index_parent_check` reports a bogus `down-link lower bound` violation.

Repro: `CREATE TABLE big(i int); INSERT … generate_series(1,500);
CREATE INDEX big_idx ON big(i); SELECT bt_index_check('big_idx')` → XX002.

The faithful default is the index's *own* comparator — the format's
`indexFormat.compare`, which is `CompareKeys` under the blob format (unchanged
behavior for blob indexes) and `ComparePGIndexTuples` (key attributes + heap-TID
tiebreak, the `_bt_compare` analog) under the tuple format. This is also what
upstream does: every comparison amcheck makes runs through the index's support
function 1, and for a built-in opclass that IS the engine ordering.

Three sites in `internal/access/amcheck`:

- `VerifyBtreeItemOrderCmp` — nil default becomes `keyFmt.Compare` (newly
  exported `IndexFormat.Compare` → `indexFormat.compare`).
- `VerifyBtreeParentDownlinks` — same default; the parent's truncated pivot
  separators then compare under the attrs/truncation rules instead of raw bytes.
- `VerifyBtreeUnique` — the nil default becomes `keyFmt.CompareKeyAttrs`
  (attrs-only equality): the tier asks "same key?" for duplicate grouping, and
  under the tuple format bytewise equality over whole tuples can never fire,
  silently disabling checkunique. `CompareKeyAttrs` is exactly
  `_bt_keep_natts`-style equality; under blob it is byte-identical to
  `CompareKeys`.

An explicitly supplied opclass `KeyComparator` still wins (the
`005_opclass_damage` mechanism): user-opclass indexes keep the blob format
(`buildPGIndexKeyDesc` refuses them), so the routine-based comparator still
decodes bare key bytes and is unaffected.

## Non-goals / unchanged

- `heapallindexed`, `rootdescend` tiers stay deferred (pre-existing ledger rows).
- No production-format changes; the on-disk tuple format is correct — only
  amcheck's read-side comparator drifted.
- System-catalog per-`CREATE DATABASE` registration (bo's ledger row) is
  untouched.

## Verification

- Fresh-cluster repro of both bugs above, then the ported suite:
  `go test -v -run 'TestPort_PgAmcheck' ./internal/testport/` — healthy
  baselines must stop reporting corruption/missing relations.
- `go test ./internal/access/amcheck/ ./internal/executor/` plus
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`,
  `scripts/tpch-spotcheck.sh` (executor-adjacent change).
