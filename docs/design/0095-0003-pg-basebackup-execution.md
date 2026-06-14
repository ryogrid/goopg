# M0095-0003 — pg_basebackup execution against goopg (010 backup case)

Status: accepted (2026-05-14).
Milestone: M0095-0003 (Client-Tools TAP Test Porting — pg_basebackup family).
Related: M0102-0002 (BASE_BACKUP wire protocol).

## Goal

Make `pg_basebackup` from PostgreSQL 18.3 successfully clone a running goopg
primary into an empty data directory using the minimum-viable option set
(`-X none --no-manifest --no-sync`). This is the "actual backup" sub-case of
upstream's `postgres/src/bin/pg_basebackup/t/010_pg_basebackup.pl` and is the
prerequisite for the Scenario-B half of M0102-0007 (goopg primary ↔ PG
standby failover).

## Background

`internal/server/basebackup.go` (M0102-0002) implements the BASE_BACKUP
replication command's wire shape — start-LSN result-set, tablespace list,
CopyOutResponse + tar frames, stop-LSN result-set — verified by
`TestBaseBackupWireProtocolFraming`. That test exercises the framing through
an in-process protocol harness; it does not link against libpq, so divergences
between goopg's wire and what the real client expects only surface here.

When this work began, `pg_basebackup -X none --no-manifest` failed against a
live goopg cluster with two distinct errors at three different stages:

1. `error: could not send replication command "SHOW data_directory_mode": ERROR: unrecognized configuration parameter "data_directory_mode"`
2. `error: could not send replication command "SHOW wal_segment_size": ERROR: unrecognized configuration parameter "wal_segment_size"`
3. `error: WAL segment size could not be parsed` (after the GUC was added).
4. `error: final receive failed:` (empty error string) once SHOWs returned values.

Each error mapped to a concrete gap; this design enumerates the four fixes
that close them.

## Fixes

### 1. Add `data_directory_mode` GUC

`pg_basebackup` issues `SHOW data_directory_mode` very early in its handshake
to know whether the source cluster expects group-readable permissions on the
data directory. Upstream sets it from the actual mode (`0700` or `0750`); goopg
v0 always creates the data dir with mode `0700`.

Registered in `internal/config/defaults.go` as:

- `Name: "data_directory_mode"`, `Type: TypeInt`, `BootVal: "448"` (= 0o700),
  `MinVal: 0`, `MaxVal: 511`, `Context: ContextInternal`, `Flags: FlagDisallowInFile`.

The value is stored decimally (448, not the string "0700") because the GUC
range-validator rejected "0700" as out-of-range — the BootVal string is parsed
as base-10 even when the configured range is mode-shaped.

### 2. Add `summarize_wal` GUC

`pg_basebackup` (PG17+) issues `SHOW summarize_wal` to decide whether
incremental backups via the upstream WAL summarizer are available. goopg v0
has no walsummarizer (full M0095-0002 work tracks that subsystem); the GUC
reports `off`.

Registered in `internal/config/defaults.go` as:

- `Name: "summarize_wal"`, `Type: TypeBool`, `BootVal: "off"`,
  `Context: ContextSigHup`.

### 3. Add `wal_segment_size` GUC reporting `16MB`

`pg_basebackup` issues `SHOW wal_segment_size` and parses the response with
`sscanf("%d%s")` — number followed by unit suffix. The naive registration
(`Type: TypeInt, Unit: UnitBytes, BootVal: "16777216"`) canonicalised to the
raw integer `"16777216"` and pg_basebackup rejected it with
`"WAL segment size could not be parsed"`.

Registered as a pre-formatted string:

- `Name: "wal_segment_size"`, `Type: TypeString`, `BootVal: "16MB"`,
  `Context: ContextInternal`, `Flags: FlagDisallowInFile`.

A general "render int GUCs with unit suffix on SHOW" pass is out of scope; the
pre-formatted string is the minimum that satisfies pg_basebackup's parser and
matches what upstream's `show_unit()` produces.

### 4. Emit `EndReplicationCommand("BASE_BACKUP")` after the stop-LSN row

Once the SHOWs returned valid values, pg_basebackup advanced through the
BASE_BACKUP wire — start-LSN row, tablespace list, CopyOut stream, tar bytes,
CopyDone, stop-LSN row — but failed with `"final receive failed: "` (empty
error string).

Empty error string from `PQerrorMessage(conn)` on a result whose status is
`PGRES_FATAL_ERROR` typically means `PQgetResult` returned NULL — the
connection is healthy but the server didn't queue another result.

Tracing the upstream walsender command dispatcher
(`postgres/src/backend/replication/walsender.c:2136`):

```c
case T_BaseBackupCmd:
    cmdtag = "BASE_BACKUP";
    set_ps_display(cmdtag);
    PreventInTransactionBlock(true, cmdtag);
    SendBaseBackup((BaseBackupCmd *) cmd_node, uploaded_manifest);
    EndReplicationCommand(cmdtag);
    break;
```

And `EndReplicationCommand` (`postgres/src/backend/tcop/dest.c:205`):

```c
void
EndReplicationCommand(const char *commandTag)
{
    pq_putmessage(PqMsg_CommandComplete, commandTag, strlen(commandTag) + 1);
}
```

So upstream emits a trailing `CommandComplete("BASE_BACKUP")` after the
stop-LSN result-set and before `ReadyForQuery`. pg_basebackup at
`pg_basebackup.c:2199` reads that frame as `PGRES_COMMAND_OK`; without it
libpq processes `ReadyForQuery` immediately and returns NULL, surfacing as the
empty "final receive failed".

`replyBaseBackup` now emits `WriteCommandComplete("BASE_BACKUP")` immediately
before `WriteReadyForQuery`. `TestBaseBackupWireProtocolFraming`'s trailer
assertion updated from `T/D/C/Z` (4 frames) to `T/D/C/C/Z` (5 frames).

## Verification

- `TestBaseBackupWireProtocolFraming` (in-process framing) — pinned to the
  new 5-frame trailer.
- `TestPort_PgBasebackup010BackupExecution` (new) — drives a live goopg
  cluster end-to-end with the real `postgres/local_install/bin/pg_basebackup`
  binary; passes with `-X none --no-manifest --no-sync`; verifies extracted
  `backup_label`, `global/pg_control`, and `PG_VERSION`.
- `TestPort_PgBasebackup010StreamWAL` — `-X stream` variant (M0102 walsender).
- `TestPort_PgBasebackup010FetchWAL` (new, 2026-06-14) — `-X fetch` variant;
  server appends the in-range WAL to the tar (see "WAL inclusion" below).
- `TestPort_PgBasebackup010Manifest` (new, 2026-06-14) — runs pg_basebackup
  WITHOUT `--no-manifest` (default CRC32C manifest); independently recomputes
  every `Files[]` CRC32C and the SHA-256 `Manifest-Checksum`, then runs the
  upstream oracle `pg_verifybackup -n` over the extracted backup, which
  accepts it.

## Backup manifest (`--manifest`, 2026-06-14)

pg_basebackup requests `MANIFEST 'yes'` by default (only `--no-manifest`
disables it), so the original `--no-manifest`-only support was a stop-gap.
The server now emits a PG-version-2 backup manifest, mirroring
`backup_manifest.c` + `bbsink_copystream`'s manifest framing.

Wire framing (`basebackup_copy.c:260-292`): after the last tar archive byte
and before `CopyDone`, the server sends a `CopyData('m')` begin-manifest
marker, then the manifest bytes through the same `CopyData('d' …)` framing as
the tar (`streamBackupManifest`). No explicit terminator — the existing
`CopyDone` closes the stream.

Manifest document (`buildBackupManifest`, byte-for-byte ordering from
`backup_manifest.c`):

- `{ "PostgreSQL-Backup-Manifest-Version": 2, "System-Identifier": <id>, …`
  — `System-Identifier` is `initdb.LoadOrCreateSystemID(DataDir)`, the same
  value written into `global/pg_control`, so pg_verifybackup's
  system-identifier cross-check passes.
- `"Files": [ … ]` — one entry per shipped file (`backup_label`, every
  base/global file, `global/pg_control`) with `Path`/`Size`/`Last-Modified`
  (GMT) and, by default, `Checksum-Algorithm: CRC32C` + `Checksum`. WAL
  segments under `pg_wal/` are **omitted** from `Files[]` (PG tracks WAL only
  via `WAL-Ranges`; a concurrent `-X stream` rewrites those segments client
  side, which would otherwise break checksum verification).
- `"WAL-Ranges": [ { "Timeline", "Start-LSN", "End-LSN" } ]` — start = the
  backup-label start LSN (0-based), end = `WrittenLSN()-1`.
- `"Manifest-Checksum": "<sha256hex>"}` — SHA-256 over every byte up to but
  not including the `"Manifest-Checksum"` field (always SHA-256 regardless of
  the per-file algorithm, the `SendBackupManifest` invariant).

CRC32C parity detail: PG's `pg_checksum_final` `memcpy`s the native
(little-endian on amd64) 4-byte `pg_crc32c` value before hex-encoding, and its
INIT/FIN convention matches Go's `crc32.Checksum` with the Castagnoli table —
so the manifest hex is `binary.LittleEndian` of the CRC32C value.

`MANIFEST_CHECKSUMS` is parsed and honoured: `NONE` (omit `Checksum-*`
fields), `CRC32C` (default), and `SHA224/256/384/512`. An unrecognised
algorithm is rejected early with `FeatureNotSupported`. `force-encode`
(`--manifest-force-encode`) and any non-UTF-8 path switch the entry to
`"Encoded-Path": "<hex>"`, matching `AddFileToBackupManifest`.

## WAL inclusion (`-X fetch`, 2026-06-14)

`pg_basebackup -X fetch` (FETCH_WAL) sends the BASE_BACKUP `WAL` boolean
option (`pg_basebackup.c:1905-1906`,
`AppendPlainCommandOption(&buf, ..., "WAL")`) and, unlike `-X stream`, opens
**no** second replication connection: the WAL needed to make the backup
self-consistent must be appended to the data tar by the server. goopg now
mirrors upstream's `includewal` block (`basebackup.c:408-560`):

- **`WAL` option parsing** (`baseBackupOptions.IncludeWAL`) — accepted in both
  the PG15+ option-list form (a bare `WAL` flag; `parseOptionBool` honours an
  explicit `'f'`/`off`/`0` false) and the legacy keyword form.
- **pg_wal is never walked.** The directory walk now ships `pg_wal` as an
  *empty* directory plus the `pg_wal/archive_status` and `pg_wal/summaries`
  empty subdirs PG emits (`sendDir():1385-1407`). Previously goopg shipped the
  full `pg_wal` contents on *every* backup (a deviation); now WAL is only
  present when the client asked for it — matching PG for `-X none`/`-X stream`
  (the stream path rewrites `pg_wal` client-side anyway).
- **In-range segment append** (`appendWALSegments`). When `IncludeWAL` is set,
  after the data files + `global/pg_control` the server appends the segments in
  the inclusive range `[XLByteToSeg(startptr), XLByteToPrevSeg(endptr)]` under
  `pg_wal/`, oldest first, where `startptr` is the 0-based redo point and
  `endptr` is the 0-based stop LSN (`WrittenLSN()-1`). goopg's on-disk WAL
  names are already PG-compatible (`wal.formatSegmentName` →
  `%08X%08X%08X`), so segment selection parses each name via
  `wal.ParseXLogFileName` and compares segment numbers — no goopg→PG name
  conversion is required. The upstream contiguity sanity check
  (`basebackup.c:484-520`) is preserved: a missing segment inside the range is
  a hard error naming the first absent file. Any `*.history` files are shipped
  too (always, per upstream). Appended WAL is deliberately **not** recorded in
  the manifest `Files[]` — PG tracks it via `WAL-Ranges` only.

Verified by `TestPort_PgBasebackup010FetchWAL` (new): drives the real
`pg_basebackup -X fetch` against a live goopg cluster and asserts the extracted
`backup_label`/`global/pg_control`/`PG_VERSION` **plus** that the START WAL
segment named in `backup_label` is present among the fetched `pg_wal/`
segments — the defining invariant that the WAL came from the server tar (fetch
streams nothing separately). `-X none`/`-X stream`/manifest tests still pass
(no regression). Parser cases added to `TestBaseBackupParseOptions`.

## pg_receivewal streaming tier (BB-020) + READ_REPLICATION_SLOT

`020_pg_receivewal.pl` drives the `pg_receivewal` client, which streams WAL
to a directory over the physical replication protocol. Previously only the
CLI/option-validation tier was ported; the slot-management + streaming tier
was deferred "pending replication protocol in goopg". That protocol now
exists (the same physical walsender path `pg_basebackup -X stream` uses), so
`TestPort_PgReceivewal020` now reproduces upstream's
**create-slot → stream → drop** sequence end-to-end against a live cluster:

1. `pg_receivewal --slot S --create-slot` → `CREATE_REPLICATION_SLOT S PHYSICAL`;
   the slot is then asserted present in `pg_replication_slots` (`slot_type =
   'physical'`).
2. `pg_receivewal --slot S -D dir` streams WAL into `dir` while the test
   generates WAL; the test asserts a 24-hex WAL segment (optionally
   `.partial`, since pg_receivewal only renames on a segment boundary) lands
   in `dir`, then kills the streamer.
3. `pg_receivewal --slot S --drop-slot` → `DROP_REPLICATION_SLOT S`; the slot
   is asserted gone.

**Engine gap closed: `READ_REPLICATION_SLOT`.** Step 2 surfaced a real gap —
`pg_receivewal`, when streaming *with* a slot, issues `READ_REPLICATION_SLOT
<name>` (PG 15+, `walsender.c:ReadReplicationSlot`) before
`START_REPLICATION` to learn the slot's restart LSN/timeline. goopg's
walsender rejected it with a syntax error and pg_receivewal looped on
reconnect. `internal/server/replication.go` now handles it
(`replyReadReplicationSlot`): a single three-column row
`(slot_type text, restart_lsn text, restart_tli int8)` — `('physical', 'X/X',
1)` for an existing physical slot (NULL LSN/tli when no position is reserved),
all-NULL for an absent/invalidated slot, and `feature_not_supported` for a
logical slot — mirroring upstream verbatim. Covered by
`TestReplicationReadReplicationSlot` (server unit test, the sibling of the
CREATE/DROP slot tests). `restart_tli` is always 1 because goopg operates on a
single timeline (see `0005-0001`).

## Out of scope (follow-ups)

- Server-side compression (gzip/lz4/zstd) — needs `bbsink_gzip/lz4/zstd`
  parity.
- Tablespaces beyond the default — out of v0 scope (M0095-0003 011 stays
  deferred).
- In-place tablespace inspection — same blocker as above (BB-011).
- Group-readable data directories (`data_directory_mode = 0o750`) — needs
  initdb permission-mask support; out of v0 scope.
