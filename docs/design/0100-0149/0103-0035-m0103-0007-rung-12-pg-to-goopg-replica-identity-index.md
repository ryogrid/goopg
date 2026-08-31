# 0103-0035 — M0103-0007 rung 12 (PG → goopg): REPLICA IDENTITY USING INDEX

Status: accepted (2026-05-14)

## Context

Rungs 1–11 of M0103-0007 verified the apply worker against tables whose
REPLICA IDENTITY was either DEFAULT (the table's PRIMARY KEY columns) or
FULL (all columns). Both cases have been pinned end-to-end:

- DEFAULT — rungs 1–3, 6–11 (PRIMARY KEY tables)
- FULL    — rung 4 (no-PK tables with full pre-image in OldTuple)

The third upstream REPLICA IDENTITY mode — `USING INDEX <unique_index>` —
remained untested. Under USING INDEX the publisher selects a non-PRIMARY
unique index as the row-locator, and pgoutput sets bit 0
(`LOGICALREP_IS_REPLICA_IDENTITY`) on each Relation-message column that
participates in that index. When such an UPDATE does not modify any
identity column, pgoutput omits the OldTuple section entirely — exactly
the same wire shape as rung 2's REPLICA IDENTITY DEFAULT + no-key-change
UPDATE, but with the identity columns belonging to a NON-PK index rather
than the PK.

Before this rung, the apply worker resolved that no-OldTuple case
through `primaryKeyOnlyRow(cat, tbl, newRow)`, which walked the
subscriber-side catalog for `idx.Primary == true` and built a partial
key from the PK columns. Two correctness gaps fell out:

1. **No-PK + USING INDEX**: the subscriber may declare no PRIMARY KEY
   at all (mirroring the publisher). `primaryKeyOnlyRow` returned `nil`
   and the UPDATE was silently dropped — pgoutput's wire bytes said
   "find the row by columns A, B", but the apply worker had no way to
   know which columns those were.

2. **PK-mismatched USING INDEX**: even with a PK on the subscriber,
   `primaryKeyOnlyRow` would key off the subscriber PK columns, which
   may not be the columns the publisher used as the identity. The
   match-by-key scan would then fail to find the pre-image row (no
   match) or, worse, match the wrong row if the subscriber PK columns
   happened to carry the same values in the new tuple.

PG's apply worker handles this correctly because it resolves the
identity columns from the Relation message's per-column flag byte
(`LOGICALREP_IS_REPLICA_IDENTITY`), not from any subscriber catalog
lookup. The fix below mirrors that design.

## Decision

Introduce `replicaIdentityKeyRow(remoteCols []wal.DecodedAttr,
localCols []catalog.Column, newRow Row) Row` in
`internal/executor/applyworker.go`. The helper walks `remoteCols`;
for each entry whose `Flags & 0x01` is set, it resolves the matching
local column position by name (the same name lookup
`decodePgoutputTupleAsRow` uses) and copies the value from `newRow`
into that slot of the returned key row. All other slots stay
`NullDatum`, which `rowMatchesKey` treats as "don't care", so the
heap match restricts to identity columns regardless of whether they
form a PK or a non-PK unique index.

`applyUpdate`'s no-OldTuple branch is rewritten to call
`replicaIdentityKeyRow` first. When the publisher emits no identity
flags at all — an edge case for older or corrupt streams — the
function returns `nil` and we fall back to `primaryKeyOnlyRow` for
defence in depth.

Constraints satisfied:

- **Symmetric with rung 2's hot path**: REPLICA IDENTITY DEFAULT
  (PRIMARY KEY) sets identity flags exactly on the PK columns, so
  `replicaIdentityKeyRow` produces the same key as
  `primaryKeyOnlyRow` for the rung-2 case. No regression to rungs
  1–11.
- **No new dependency on subscriber catalog**: the lookup is purely
  by remote-column name; the subscriber's catalog only contributes
  the column list for ordinal resolution. A subscriber with no PK
  at all is fully supported.
- **Wire-bit-only signal**: the function does not consult the
  Relation message's `Replident` byte (`'d'`/`'f'`/`'i'`/`'n'`).
  Upstream pgoutput sets the per-column flag identically regardless
  of which of the three identity-with-key modes triggered, so the
  per-column flag is the canonical signal.

## Implementation summary

`internal/executor/applyworker.go`:

- New `replicaIdentityKeyRow(remoteCols, localCols, newRow) Row`. Returns
  `nil` when no remote column carries the identity flag or when `newRow`
  doesn't span the full local column count (defensive).
- `applyUpdate`'s `len(m.OldTuple) == 0` branch:
  `oldKeyRow = replicaIdentityKeyRow(r.remote.Columns, r.local.Columns, newRow)`;
  if `oldKeyRow == nil`, fall back to `primaryKeyOnlyRow(w.cat, r.local, newRow)`.

`internal/executor/applyworker_test.go`:

- `TestReplicaIdentityKeyRow` — three sub-cases:
  - PK columns flagged: key carries id, NULL elsewhere.
  - Non-PK unique-index columns flagged (composite identity over (a, v)):
    key carries both, no other columns.
  - All flags zero: returns nil so `applyUpdate` falls back to
    `primaryKeyOnlyRow`.

`internal/testport/pgoutput_interop_test.go`:

- `TestPort_PgoutputInteropPGToGoopgReplicaIdentityUsingIndex` —
  publisher table without a PRIMARY KEY but with a UNIQUE index on `(k)`,
  `ALTER TABLE public.t REPLICA IDENTITY USING INDEX t_k_uniq`. Workload:
  three INSERTs, one UPDATE that does not touch `k`, one DELETE keyed on
  `k`. Assertions through fresh `database/sql` sessions verify the
  identity-column row-locator works end-to-end on the subscriber.

## Verification

- `go test -count=1 -timeout 60s
  -run "TestApplyWorker|TestReplicaIdentityKeyRow|TestPrimaryKeyOnlyRow|TestApplyUpdateByKey"
  ./internal/executor/` — PASS.
- `go test -count=1 -timeout 180s
  -run TestPort_PgoutputInteropPGToGoopgReplicaIdentityUsingIndex
  ./internal/testport/` — PASS.
- Spot regression sweep on `./internal/executor/ ./internal/wal/
  ./internal/catalog/ ./internal/testutil/pubsubcluster/` — green.

## Follow-ups (deferred within M0103-0007)

- DEFAULT-expression evaluation for subscriber-extra INSERTs (rung 11
  follow-up).
- pgbench against PG publisher with `pgbench_history` polling.
- proto_version=2 streaming subxacts.
- kill -9 + libpq multi-host reconnect plumbing on the client side.
