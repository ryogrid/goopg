# M0103-0007 rung 5 — PG-publisher → goopg-subscriber unchanged-TOAST ('u') decode

## Status

accepted

## Context

Rungs 1–4 of M0103-0007 closed the apply path for tuples whose columns
were all encoded as `'t'` (text-format value) or `'n'` (NULL). Upstream
pgoutput emits a third per-column status code that the goopg apply
worker has, until now, hard-rejected:

  - **`'u'` — unchanged TOAST**. Emitted in two places:
    1. The **NewTuple of an UPDATE** when (a) the column has external
       or extended storage that resolved to a TOAST pointer, and
       (b) the UPDATE did not modify that column. Upstream's encoder
       (`postgres/src/backend/replication/logical/proto.c::logicalrep_write_tuple`)
       writes `'u'` + zero length: "I am telling you this column
       didn't change; reuse the value the subscriber already has."
    2. The **OldTuple ('O' / 'K' section)** for the same column. Under
       REPLICA IDENTITY DEFAULT the OldTuple is omitted entirely when
       no key columns changed, so this only surfaces under REPLICA
       IDENTITY FULL — the pre-image of an unchanged-TOAST column is
       sent as `'u'`, not as a `'t'`-prefixed payload.

The decoder side (`internal/wal/pgoutput_decoder.go:277`) has accepted
`'u'` since M0103-0003 — it returns `DecodedColumn{Status: 'u',
Bytes: nil}`. The apply side
(`internal/executor/applyworker.go::decodePgoutputTupleAsRow`) returns
`"col %q: 'u' (unchanged TOAST) status not supported"` for every `'u'`
cell it sees. Any real-world workload that touches a table with a
TOASTed column (large text/bytea, e.g. `payload text` storing a 10 KB
JSON blob) will eventually hit this error and stall the apply slot:
the publisher legitimately emits `'u'` for non-modified TOAST columns,
and we have no way to recover without re-streaming the entire row.

Rung 4 explicitly deferred this: "TOAST columns (`'u'` status code)
… TOAST support lands as its own rung."

## Decision

Plumb a parallel **unchanged-mask** through the apply path, and fill
the `'u'` cells of an UPDATE's NewTuple from the matched heap row
before insert.

### Decoder signature

`decodePgoutputTupleAsRow` returns `(Row, []bool, error)`. The second
return is the same length as `localCols`; the bool at position *i* is
`true` iff `tup[i].Status == 'u'`. The Row cell at position *i* for
`'u'` is `NullDatum` (mirroring `'n'`) — the row-locator paths that
treat `NullDatum` as "don't care" via `rowMatchesKey` then naturally
let `'u'` cells act as wildcards in the OldTuple match, which is the
correct semantics: the publisher didn't tell us the value, so we must
match on the columns it did tell us about.

`'u'` in an INSERT NewTuple stays rejected as before — pgoutput's
encoder never emits `'u'` for INSERTs (there is no pre-existing heap
row to inherit from), and accepting it would silently install a NULL
in place of real data.

### apply-worker UPDATE flow

`applyUpdate` decodes NewTuple as `(newRow, newUnchanged, _)` and
threads `newUnchanged` into a new parameter of `applyUpdateByKey`.
OldTuple decode discards the mask (a `'u'` OldTuple cell becomes
`NullDatum`, which `rowMatchesKey` already treats as wildcard — no
new helper needed).

`applyUpdateByKey` gains a first-scan pass when `newUnchanged` has
any true entries. The pass is a read-only variant of the existing
match scan:

  - Walks every block of `rel`, decodes each visible tuple via
    `DecodeRowInto`, and returns the first row that matches
    `oldKeyRow` under `rowMatchesKey`.
  - The matched row is a copy (the buffer holding the page is
    unpinned before return), so no buffer-lifetime concerns.

For each `i` where `newUnchanged[i]` is true, `newRow[i]` is
overwritten with the matched row's value at the same position. The
existing `applyDeleteByKey` + `writeHeapRowReturning` + index
maintenance then proceeds with a fully-decoded `newRow`. If the scan
finds no match, the `'u'` cells stay `NullDatum`; the existing
applyDeleteByKey/writeHeap path produces a stray "INSERT-from-no-match"
row exactly like the pre-existing no-match UPDATE behavior — preserved
for this rung, since fixing it is a separate concern.

The two-scan cost (first read-only match scan, then the xmax scan
inside `applyDeleteByKey`) is acceptable: tables that exercise this
path tend to be wide (TOAST is only allocated above ~2 KB per row)
and small in the apply-test scenarios. The scan only runs when at
least one `'u'` cell is present, so the rung-1–4 hot path (no TOAST)
is unaffected.

### Why not "fill from OldTuple"?

Under REPLICA IDENTITY FULL the OldTuple carries a `'u'` for the same
column (it's unchanged, so upstream short-circuits it on the pre-image
side too). Under DEFAULT the OldTuple is omitted entirely or contains
only the key columns. Neither shape supplies the value. The heap is
the sole source of truth on the subscriber side.

## What this pins

Two new tests are added.

### Unit: `TestApplyWorkerDecodeReturnsUnchangedMask`

Exercises `decodePgoutputTupleAsRow` directly with a synthetic tuple
containing one `'t'`, one `'n'`, one `'u'` cell. Verifies:

  - the returned Row has the decoded value in slot 0, `NullDatum` in
    slot 1 and slot 2,
  - the returned mask is `[false, false, true]`,
  - INSERTs through the apply worker still reject `'u'` (defensive —
    no real publisher would emit this, but a corrupt stream must
    not silently install a NULL).

### Live E2E: `TestPort_PgoutputInteropPGToGoopgUnchangedToast`

Same harness as the rung-3/4 tests (`pubsubcluster.NewMixed`, PG pub
+ goopg sub, pre-created logical slot). Schema:

```sql
CREATE TABLE public.t (id int PRIMARY KEY, name text, payload text);
ALTER TABLE public.t ALTER COLUMN payload SET STORAGE EXTERNAL;
```

`SET STORAGE EXTERNAL` forces the payload column out-of-line into the
TOAST relation, guaranteeing that any UPDATE that doesn't touch
`payload` will surface as `'u'` on the wire (rather than as an inline
`'t'`-typed value).

Workload:

  - INSERT `(1, 'alpha', repeat('X', 4096))` — payload large enough to
    TOAST.
  - UPDATE `SET name = 'alpha-updated' WHERE id = 1` — does not touch
    `payload`; the wire shape is `'U' relOid 'N' (t,t,u)`.

Assertions on the goopg subscriber via fresh `database/sql` sessions:

  - `count(*) WHERE id = 1` → 1.
  - `name WHERE id = 1` → `'alpha-updated'`.
  - `length(payload) WHERE id = 1` → 4096 (TOAST preserved; not
    replaced by NULL).
  - `substr(payload, 1, 1) WHERE id = 1` → `'X'` (content preserved).

The `length` + `substr` assertions are essential: a NULL-fill bug
would manifest as `length=NULL` or zero, and a wrong-cell bug would
yield a different content character.

## Verification

```
go test -count=1 -timeout 60s -run TestApplyWorkerDecodeReturnsUnchangedMask \
  ./internal/executor/
go test -count=1 -timeout 120s -run TestPort_PgoutputInteropPGToGoopgUnchangedToast \
  ./internal/testport/
```

Both must PASS. The four pre-existing `TestPort_PgoutputInterop*`
tests (rungs 1–4) must remain green to confirm no regression on the
`'t'`/`'n'` paths.

## Out of scope

  - **TOAST 'u' on INSERT**. pgoutput's encoder never emits it; we
    keep rejecting it defensively. If a future protocol revision adds
    it, we revisit.
  - **The "no-match UPDATE produces an INSERT" bug**. `applyUpdateByKey`
    has long behaved this way (no-match UPDATE delete-no-ops then
    inserts the new row); this rung does not fix it. It only ensures
    that when a match *is* found, the `'u'` cells are filled from it.
  - **TOAST 'u' on DELETE OldTuple**. The OldTuple under FULL can also
    carry `'u'`; the existing `rowMatchesKey` "skip NULL key cells"
    rule already treats this correctly (no new wiring needed). Under
    DEFAULT the OldTuple section is omitted or contains only the key
    columns, neither of which can be `'u'` (PK columns can't be
    TOASTed in practice).
  - **`pgbench` workload**, **`kill -9` + libpq multi-host reconnect**,
    **DDL replication**. Slated for later rungs.
