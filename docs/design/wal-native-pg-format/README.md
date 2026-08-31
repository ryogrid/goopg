# WAL native → PostgreSQL 18.3 format-parity spec bundle

| Field      | Value                                                              |
| ---------- | ----------------------------------------------------------------- |
| Status     | draft (reference spec; no code change in this bundle)             |
| Date       | 2026-07-15                                                        |
| Branch     | `wal-system-pgnize`                                                |
| Oracle     | PostgreSQL 18.3 — the code tree under [`postgres/`](../../../postgres/) (`PACKAGE_VERSION "18.3"`) |
| Purpose    | Reference specs to drive goopg's **native** WAL format to byte-parity with PG 18.3 |

## Why this bundle exists

goopg writes its Write-Ahead Log in a **native, goopg-specific record format**.
It *can* also emit PG-shaped "canonical" records, but only behind the
`GOOPG_WAL_CANONICAL` knob (default **off**), and even then only for a narrow
subset of record kinds. The goal of the wider effort is to make goopg's
**native** WAL byte-identical to PostgreSQL 18.3 — both **on disk** and **over
the wire** (logical replication / `pgoutput`) — so the native/canonical split
can eventually be collapsed and any stock PG 18.3 tool (`pg_waldump`, a PG
standby, a logical subscriber) can consume goopg's WAL directly.

This bundle is **documentation only**. It produces the reference material a
later implementation effort will follow; it changes no executable code.

## The two things that are already parity vs. the one that is not

goopg's WAL has two independent layers:

- **Frame / envelope** — the WAL page headers (magic `0xD118`, short/long
  `XLogPageHeaderData`), `MAXALIGN` padding, the 24-byte `XLogRecord` header,
  and the `CRC32C` checksum. In PG-compat mode this layer is **already
  field-faithful** to PG 18.3 (`internal/wal/xlog_record.go`,
  `internal/wal/xlog_emit.go`, `internal/wal/xlog_page.go`).
- **Record content** — the per-kind *main data* struct and the *block
  reference* framing that sit inside the envelope. This is where goopg's native
  format **diverges** from PG: native records carry a goopg `RecordKind` byte
  and goopg-specific field layouts, and on disk are all tagged
  `RM_XLOG` / `info = 0xF0` so a stock PG standby skips them.

**The correction target is the record content**, not the frame. These docs
specify, per record kind, the exact PG 18.3 content layout the native encoder
must produce.

## Documents

| Doc | Title | What it delivers |
| --- | ----- | ---------------- |
| [01](01-emitted-wal-record-inventory.md) | Emitted WAL record inventory | Every record kind goopg **currently emits** (disk + network), grouped by PG-analog / goopg-private / canonical / pgoutput, with emit-site citations. |
| [02](02-wal-schema-dsl-spec.md) | WAL schema DSL spec | A kaitai-struct–inspired DSL for describing WAL record byte layouts, plus the WAL-specific extensions kaitai lacks. The notation doc 03 is written in. |
| [03](03-pg183-wal-record-schemas.md) | PG 18.3 WAL record schemas | The PG 18.3 target byte layout for each in-scope record kind, written in the doc-02 DSL, each field cited to `postgres/src/...`, with a per-record "native vs PG delta" note. |
| [04](04-remove-canonical-and-pg-rmgr-dispatch.md) | Remove canonical WAL + knob + skip-tag; dispatch on PG-compatible (xl_rmid, xl_info) | **Actionable implementation plan** (not reference-only like 01-03): removes the `0xFE` canonical record family, the `GOOPG_WAL_CANONICAL` knob, and the `RM_XLOG`/`0xF0` skip-tag, replacing classification and recovery dispatch with a real PG-style `(rmgr, opcode)` table. Record *body* content stays native (the 01/03 content rewrite is explicitly out of scope here) — this doc only removes the goopg-special scaffolding around the already-PG-faithful frame. Agent-reviewed against code + PG source 2026-07-15 (2 blockers + 1 major + 5 minor folded in). |

Read 01-03 in order: 01 scopes *what* must change, 02 defines *how the target is
written down*, 03 is *the target itself*. Doc 04 is a separate, actionable
removal/rework plan that can land ahead of the 01/03 content rewrite.

## Scope note

Doc 01 catalogues **all** currently-emitted kinds. Doc 03 gives a PG 18.3
schema only for the kinds that **have** a PG WAL analog (heap / heap2 / btree /
xact / xlog / standby / clog / smgr, plus the `pgoutput` messages). goopg's
**private catalog-DDL records** (e.g. `CreateDatabase`, `CreateIndex`,
`SequenceState`) have no stock-PG WAL equivalent — PG journals catalog changes
as ordinary heap-tuple operations on `pg_catalog` tables — so they are listed in
doc 01 but explicitly out of scope for doc 03's schema mapping.

## Related existing design docs

- [`0101-0001-wal-page-header-compat-default.md`](../0100-0149/0101-0001-wal-page-header-compat-default.md) — page-frame (PageHeaders) parity, magic `0xD118`.
- [`0101-0003-wal-xlprev-restart-seeding-fix.md`](../0100-0149/0101-0003-wal-xlprev-restart-seeding-fix.md) — the `xl_prev` 0-based prev-link fix.
- [`0100-0005-loop14-pg-physical-format-fixes.md`](../0100-0149/0100-0005-loop14-pg-physical-format-fixes.md) — physical tuple/format fixes.
- [`0030-0002-ddl-wal-records.md`](../0000-0049/0030-0002-ddl-wal-records.md) — the goopg-private catalog-DDL record family.
- [`0079-0002-btree-record-wal-parity.md`](../0050-0099/0079-0002-btree-record-wal-parity.md), [`0080-0001-heap-freeze-and-multi-insert-wal.md`](../0050-0099/0080-0001-heap-freeze-and-multi-insert-wal.md) — prior per-RMGR parity work.
- `analysis/perf-optimize3-dash/01-single-stream-wal-design.md`, `02-canonical-only-coverage-audit.md` — native-vs-canonical rationale and the canonical coverage gaps.
