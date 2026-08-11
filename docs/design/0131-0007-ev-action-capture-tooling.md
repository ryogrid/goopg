# `ev_action` capture tooling — make the system-view corpus mechanical, not artisanal

**Status:** in progress — S7.1/S7.2/S7.3/S7.5/S7.6 landed 2026-08-11; S7.4 (the
Go generator) pending. See "Implementation status" and "Findings" at the end.
**Date:** 2026-08-11
**Milestone:** M0131 (S7)

## Problem

Six system views are hosted on disk today (`pg_stat_wal_receiver` 12100,
`pg_stat_replication` 12102, `pg_stat_recovery_prefetch` 12103,
`pg_stat_subscription` 12104, `pg_replication_slots` 12105,
`pg_stat_replication_slots` 12106 — `internal/initdb/relcache_init.go:688`,
`:693-697`). Each cost three hand-written artefacts: a verbatim
`nodeToString(Query)` blob embedded from `internal/initdb/*_ev_action.dat`
(`pg_rewrite_bootstrap.go:29-45`); a `nailedAttr` table transcribed
column-by-column from `system_views.sql` plus `pg_proc.dat` (e.g.
`pgStatReplicationViewAttrs`, `relcache_init.go:2593-2620`); and a `nailedRel`
row pinning OID / reltype / relkind / relnatts (struct at
`relcache_init.go:55-63`).

`system_views.sql` holds **80** views. Transcribing 74 more by hand is not a
plan. S9 needs a generator, and the generator needs an oracle proving it emits
*exactly* what the hand-written six already are.

## Design

### The tool: `scripts/capture-ev-action.sh`

`scripts/pg-oracle-diff.sh` already establishes the throwaway-oracle shape:
`#!/usr/bin/env bash`, `set -euo pipefail` (`:45`), `SCRIPT_DIR`/`REPO_ROOT`
resolution with `PG_BIN="${REPO_ROOT}/postgres/local_install/bin"` and
`PG_LIB=…/lib` exported onto `PATH`/`LD_LIBRARY_PATH` (`:47-53`), a scratch dir
under `${REPO_ROOT}/tmp/` (`:96-99`), an `EXIT INT TERM` cleanup trap (`:119`),
and `initdb -D "$DIR" --no-sync -q` (`:134`). Reuse all of it; only the queries
and emitters are new.

```
scripts/capture-ev-action.sh [--out-dir DIR] [--manifest FILE] <view> [view ...]
scripts/capture-ev-action.sh --verify      # re-derive the committed six
```

Per invocation: `initdb` a throwaway PG 18.3 cluster in
`tmp/ev-action-capture-pg-data`, `pg_ctl start` on an ephemeral port, run three
queries per view through `psql -X -A -t`, emit artefacts, then `pg_ctl stop` +
`rm -rf` on the trap. The oracle is only ever read from; no goopg process is
involved.

**Query 1 — the blob.** `SELECT r.ev_action FROM pg_rewrite r WHERE r.ev_class
= 'pg_catalog.<view>'::regclass AND r.rulename = '_RETURN';` The committed
blobs are **one line, no trailing newline**, first byte `(` and last byte `)`
(verified: `wc -l` = 0 and `tail -c1 | xxd -p` = `29` on all six). psql appends
a newline — strip exactly one and nothing else, or every capture differs from
the committed bytes by one byte.

**Query 2 — the attribute table.** `SELECT a.attnum, a.attname, a.atttypid,
a.attlen, a.attnotnull, a.attisdropped FROM pg_attribute a WHERE a.attrelid =
'pg_catalog.<view>'::regclass AND a.attnum > 0 ORDER BY a.attnum;` This maps
one-to-one onto `nailedAttr` (`relcache_init.go:65-72`: `Name`, `TypeOID`,
`Num`, `Len`, `NotNull`, `IsDropped`) — no derivation, no judgement.

**This is the field that must come from the oracle, not from reading
`system_views.sql`.** `atttypid` is the type PG's parse analyzer assigned to the
corresponding `TargetEntry`'s `Var`/`FuncExpr` *inside* `ev_action`. If the
transcribed `pg_attribute` row disagrees, the hosted PG builds a `TupleDesc`
that does not match the tuples the rule's plan produces and deforms garbage —
the `TupleDescInitEntry` / `populate_compact_attribute_internal` FATAL shape
recorded from M0106 (`tupdesc.c:105`). Capturing both from one live cluster
makes the agreement structural rather than a matter of care.

`attalign`/`attbyval` are **not** captured: `bootstrapPgAttributeTuples`
(`internal/initdb/initdb.go:2215`) derives them from the pg_type bootstrap set
(`pgTypeBootstrapEntryMap`, `pg_type_bootstrap.go:334+`, seeded by
`pgTypeAllEntries` — 193 entries, `internal/initdb/pg_type_seed_data.go:6`,
generated from upstream's 112 `pg_type.dat` base entries plus array peers).
The tool must therefore **fail loudly** on a captured `atttypid` absent from
that set rather than emit a row whose alignment cannot be resolved. (Checked
clear for the S9.1/S9.2 candidates: `numeric` 1700, `record` 2249, `pg_lsn`
3220, `interval` 1186, `inet` 869, `_text` 1009, `_oid` 1028 are all present.)

**Query 3 — the relation row.** `SELECT c.oid, c.relname, c.reltype, c.relkind,
c.relnatts FROM pg_class c WHERE c.oid = 'pg_catalog.<view>'::regclass;` Feeds
`nailedRel{OID, RelName, RelType, RelKind, RelNatts, IsShared:false, Attrs}`.
**`reltype` is the one field the tool must not copy blindly:** upstream creates
a per-view composite `pg_type` row, whereas goopg pins all six to `2249`
(RECORDOID) — `relcache_init.go:693-697`, rationale `:679-682`. Emit the
captured value into the manifest and goopg's `2249` into the Go fragment,
flagging the divergence, until M0131-S6.5's probe settles whether a real
composite reltype is required.

### The OID-mapping table

`:relid` values inside a captured blob are the *oracle cluster's* OIDs. Catalog
relids (1259, 1260, 1262, 6100, …) are pinned upstream and need no change.
View relids are initdb-assigned in the
`FirstUnpinnedObjectId..FirstNormalObjectId` band 12000..16383
(`postgres/src/include/access/transam.h:195-197`) and differ between oracle and
goopg. The tool applies an `oracleViewOID → goopgViewOID` table to every
`:relid` before writing the blob.

**That table is S8's output, not S7's.** S7 consumes it and, absent a mapping
entry for an in-band relid, refuses to emit. If S8 chooses OID pinning the
table is the identity function and this step degrades to an assertion — which
is itself an argument for pinning
(`docs/design/0131-0008-system-view-oid-policy.md`).

### Emitters

The blob goes to `internal/initdb/<view>_ev_action.dat`, picked up by a new
`//go:embed` line beside the existing six (`pg_rewrite_bootstrap.go:29-45`).

Go fragments follow the repo's existing generator convention rather than being
written by the shell: `cmd/gen-*/main.go` files carry `//go:build ignore`, run
from the repo root, and write to stdout redirected into an
`internal/initdb/*_seed_data.go` headed
`// Code generated by cmd/<tool>/main.go; DO NOT EDIT.`
(`cmd/gen-pg-proc-data/main.go:1-17`, `internal/initdb/pg_proc_seed_data.go:1-5`).
So: the shell script owns the oracle and writes `.dat` files plus a manifest
(`internal/initdb/nailed_view_manifest.tsv` — view, oracle OID, goopg OID, rule
OID, reltype, relnatts, one line per attribute), and a new
`cmd/gen-nailed-view-tables/main.go` renders that manifest into
`internal/initdb/nailed_view_seed_data.go`. Splitting there keeps the only
non-reproducible step (running a real PG) out of `go generate` and keeps the Go
emitter deterministic and diffable.

Rule OIDs (`pgRewriteOIDPg…Return`, `pg_rewrite_bootstrap.go:52-59`) are
allocated from a contiguous reserved sub-band and recorded in the manifest, so
each new `pgRewriteEntry` (`:95-104`; `EvType:'1'`, `EvEnabled:'O'`,
`IsInstead:true`, `EvQual:"<>"` are constant for every `_RETURN` rule) is
generated whole. Nothing downstream changes: `pgRewriteRow` (`:203-214`) and
`bootstrapPgRewriteTuples` (`:221-237`, via `writeMultiPageHeapRows`,
`initdb.go:6098`) already take an arbitrary-length entry list, and
`pglzVarlenaDatum` (`pglz.go:36-47`) already compresses blobs over 2048 B.

## Guards

1. **The oracle test — re-derive the existing six byte-identically.**
   `--verify` captures all six into a temp dir and `cmp`s each against the
   committed `internal/initdb/*_ev_action.dat`. Byte-identical or fail. This is
   the whole acceptance test for S7: the six were transcribed from a real PG
   18.3 and are known-good, so reproducing them proves the pipeline (query,
   newline stripping, OID mapping) faithful.
2. **The same run re-derives the attribute tables**, compared against
   `pgStatWalReceiverAttrs()` and its five siblings
   (`relcache_init.go:2593-2620` and neighbours). Drift is either a
   transcription bug in the committed table or a bug in the tool — both worth
   finding.
3. **The `12261` re-surfacing is expected and must not be suppressed.**
   `internal/initdb/pg_stat_replication_slots_ev_action.dat` contains
   `:relid 12261` twice (verified by grep). If S6 patched the blob to `12105`
   rather than repinning the view OID per S8, guard #1 *will* fail on that file
   — the oracle emits `12261`, the tree holds a patched `12105`. **That is the
   tool working.** The fix is a `12261 → 12105` row in the S8 mapping table,
   not a special case for the file.
4. **Unmapped in-band relid is a hard error** — any `:relid` in 12000..16383
   with no mapping entry aborts the capture. Same invariant M0131-S6.2 asserts
   over the committed corpus; enforcing it at capture time means the corpus can
   never lose the property again.
5. **Unknown `atttypid` is a hard error** (Query 2), not an attribute row with
   underivable `attalign`/`attbyval`.
6. **Idempotence:** two `--verify` runs against two fresh `initdb` clusters
   produce identical output. Upstream assigns `12xxx` sequentially during
   initdb, so this also empirically checks that those assignments are
   deterministic for a fixed PG 18.3 build — the assumption S8's pinning option
   rests on.
7. UNITS + SMOKE green.

## Implementation status (2026-08-11)

Landed: `scripts/capture-ev-action.sh` (S7.1 the throwaway oracle, S7.2 the
three queries, S7.3 the `.dat` + `internal/initdb/nailed_view_manifest.tsv`
emitters, S7.5 the unknown-`atttypid` hard error, S7.6 `--verify`) and
`internal/initdb/nailed_view_manifest_test.go` (guard #2, offline — the
manifest is checked in, so only re-capturing needs a real PG).

Pending: **S7.4**, `cmd/gen-nailed-view-tables/main.go`, which renders the
manifest into `internal/initdb/nailed_view_seed_data.go`. Until it exists the
`nailedRel`/`nailedAttr` tables stay hand-written in `relcache_init.go` and the
manifest *checks* them rather than *generating* them — which is why guard #2 is
worth having on its own. S8b's guards consume the same manifest.

Two deviations from the design as written, both forced by the oracle:

- **PG 18's `initdb` has no `-q`/`--quiet`** (only `-d/--debug`), contrary to
  the `scripts/pg-oracle-diff.sh:134` citation; silence it by redirecting.
- Queries use psql's `-F $'\t'` field separator rather than `||`-concatenating
  columns, because `text || "char"` is ambiguous in PG 18
  (`pg_class.relkind` — "operator is not unique").

The pinned table and the pg_type bootstrap set are parsed out of
`system_view_oid_pins.go` and `pg_type_seed_data.go` at run time rather than
duplicated in the script, so the tool cannot drift from the tree's own policy.

## Findings

**Guard #1 passed on the first complete run: all six committed blobs re-derive
byte-identically**, including `pg_stat_replication_slots`'s `:relid 12261`
(design guard #3's expected landmine — disarmed for free by S8a's repin, so no
mapping row was ever needed). Three independent `initdb` runs produced
identical output, which is guard #6's idempotence check and, with it, further
evidence that PG 18.3's `12xxx` assignments are deterministic — the assumption
Option A rests on.

**Guard #2 found a real transcription bug on its first run.**
`pgStatReplicationSlotsViewAttrs()` declared `slot_name` as `name`(19, len 64);
the oracle says `text`(25, len −1). The view selects `s.slot_name` — the OUT
parameter of `pg_stat_get_replication_slot`, whose `proallargtypes` starts
`{text,text,…}` (`pg_proc.dat:5676-5680`) — not `r.slot_name` from the
`pg_replication_slots` base view, which really *is* `name`(19)
(`system_views.sql:1045-1059`). Both the table and the hand-pinned test that
should have caught it (`TestPgStatReplicationSlotsViewAttrs`) carried the same
wrong value, because both were transcribed by the same reading of
`system_views.sql`; only an oracle capture could break the tie. This is exactly
the failure shape the design predicted — a hosted PG builds its `TupleDesc`
from these rows and would deform a `text` datum as `name`/64
(`tupdesc.c:105`) — and it stayed latent only because the view returns zero
rows with no replication slots defined, which is all M0131-S6's evaluability
assertion exercises.

The generalisable lesson is the one M0131-S6 already paid for once with
`pgSubscriptionAttrs` (9 of 18 columns): **hand-transcribed catalog shape is
wrong often enough that it needs a machine oracle, not more care.** The six
nailed views are now covered; every *other* `nailedAttr` table in
`relcache_init.go` is still hand-written and unchecked.

Noted, not fixed: goopg's own runtime virtual replication views
(`internal/initdb/replication_views.go`) declare **every** column as `text`,
including ones PG types as `name`/`int8`/`timestamptz`. That is a separate,
wholesale divergence on the runtime SELECT-side lane rather than the on-disk
nailed-catalog lane this milestone works, and it is ledgered rather than folded
in here.

## References

- M0131 implementation plan §S7, `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
- `docs/design/0131-0008-system-view-oid-policy.md` (supplies the mapping table);
  `docs/design/0131-0009-system-view-corpus-widening.md` (the consumer)
- `internal/initdb/pg_rewrite_bootstrap.go`, `internal/initdb/relcache_init.go`
- `scripts/pg-oracle-diff.sh` (throwaway-oracle conventions);
  `cmd/gen-pg-proc-data/main.go`, `cmd/gen-pg-type-data/main.go` (generator conventions)
- `postgres/src/backend/catalog/system_views.sql`,
  `postgres/src/include/catalog/pg_rewrite.h:32-45`
