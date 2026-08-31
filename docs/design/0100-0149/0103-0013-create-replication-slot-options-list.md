# 0103-0013 — `CREATE_REPLICATION_SLOT` parenthesised options list

- Status: accepted (M0103-0008 rung 8 closure, 2026-05-14)
- Milestone: M0103 — Heterogeneous logical replication failover E2E
- Sub-milestone: M0103-0008 — Scenario B (goopg primary + PG subscriber)

## Context

`TestPort_PgoutputInteropGoopgToPG` runs PG-as-subscriber against a
goopg publisher. With rungs 1–7 of M0103-0008 closed, dropping the
`t.Skip` immediately surfaced the next failing probe:

```
ERROR:  could not create replication slot "g2pg_sub":
        ERROR:  unexpected token "(SNAPSHOT" after LOGICAL pgoutput
```

`CREATE SUBSCRIPTION … WITH (enabled = true, copy_data = false)`
internally executes — over the replication protocol — the PG14+
syntax for `CREATE_REPLICATION_SLOT`:

```
CREATE_REPLICATION_SLOT "g2pg_sub" LOGICAL pgoutput (SNAPSHOT 'nothing')
```

goopg's `replyCreateReplicationSlot` was written against the legacy
pre-PG14 grammar that used positional trailing keywords
(`EXPORT_SNAPSHOT | NOEXPORT_SNAPSHOT | USE_SNAPSHOT | TWO_PHASE`) and
tokenised the args via `strings.Fields`. The opening `(` glued itself
to `SNAPSHOT` after whitespace splitting and the option-name switch
rejected it.

Upstream switched to a parenthesised, comma-separated options list
in PG14
(`src/backend/replication/repl_gram.y::create_slot_options`). The
recognised options are:

| Option        | Value                          | Notes                         |
|---------------|--------------------------------|-------------------------------|
| `SNAPSHOT`    | `'export'`, `'use'`, `'nothing'` | Snapshot disposition.       |
| `TWO_PHASE`   | optional boolean               | Logical only.                 |
| `RESERVE_WAL` | optional boolean               | Physical only.                |
| `FAILOVER`    | optional boolean               | PG17+, logical only.          |

goopg does not yet ship a snapshot exporter — `snapshot_name` in the
reply has been NULL since v0 regardless of the requested disposition.
Two-phase decoding and failover slots are not implemented. So all four
options are no-ops; the immediate fix is to **parse and accept** the
new grammar so subscription creation can proceed to the streaming
phase.

## Decision

Split the args string into a prefix (everything before the optional
`(`) and an options block (the content between balanced parens),
tokenise the prefix as before, and parse the options block with a
single-pass scanner that handles single-quoted string values.

### Why a separate scan instead of an AST-level parser

The replication-command dispatcher already lives in
`internal/server/replication.go` and tokenises every other verb
(`IDENTIFY_SYSTEM`, `DROP_REPLICATION_SLOT`, `START_REPLICATION`,
`TIMELINE_HISTORY`) with ad-hoc string scanning. Threading these
commands through `internal/parser` would couple the protocol layer
to the SQL grammar and force a much bigger refactor than this rung
warrants. The existing
`splitStartReplicationOptionList` already handles comma-splitting
outside single-quoted strings; we reuse it. The only new piece is
the prefix/options-block split, which has to be paren-aware to allow
the `START_REPLICATION` path to keep working without disturbing it.

### Unknown options must error

Acknowledging unknown option names as no-ops would silently mask
future probe rungs (e.g. the day PG18 ships a new option, the
subscriber would proceed against a publisher that didn't honour it).
Rejecting unknown options with a syntax error ensures the next probe
surfaces loudly the same way every other M0103-0008 rung has — with a
deterministic error in the live test rather than a hang.

## Implementation

### `splitReplicationSlotOptionsBlock(args string) (prefix, opts string, has bool, err error)`

Linear scan with two counters: paren depth and quote state. Tracks
the position of the outermost `(` so the prefix is returned exactly
up to (but not including) that byte. SQL doubled single-quote
escapes (`''` inside a string) are honoured so a future
`SNAPSHOT 'it''s'` value would round-trip; current PG only emits
single-word disposition values so this is defensive but cheap.
Returns errors for:

- unmatched `)` (depth would go negative)
- missing `)` (depth still positive at EOF)
- unterminated quoted string
- trailing tokens after the closing `)`

### `parseReplicationSlotOptions(raw string, kind wal.SlotKind) error`

Splits `raw` via `splitStartReplicationOptionList` (the existing
comma-aware splitter), then for each option:

- Trim whitespace; skip empty entries.
- Split into name + optional value at the first whitespace run.
- Dispatch on the upper-cased name:
  - `SNAPSHOT` — value required; accept `'export'`, `'use'`,
    `'nothing'` (case-insensitive, single-quote stripped); reject
    other values.
  - `TWO_PHASE` — value optional, no-op.
  - `RESERVE_WAL` — reject on `wal.SlotLogical`; no-op on physical.
  - `FAILOVER` — reject on `wal.SlotPhysical`; no-op on logical.
  - default — return `unrecognised option <name>`.

The kind-vs-option cross-checks mirror upstream's
`parse_create_replication_slot_options` logic.

### `replyCreateReplicationSlot` wiring

1. Call `splitReplicationSlotOptionsBlock` on the raw args, capturing
   the prefix and (optional) options block before any further
   tokenisation.
2. `strings.Fields` the prefix as before; the slot-name / TEMPORARY
   / PHYSICAL / LOGICAL / plugin parse is unchanged.
3. The legacy positional trailing keywords stay — older clients that
   still send `LOGICAL pgoutput NOEXPORT_SNAPSHOT` keep working.
4. If the options block is present, call `parseReplicationSlotOptions`
   with the resolved `wal.SlotKind` so kind-vs-option mismatches are
   caught.

The reply-shape code is untouched — `snapshot_name` is still NULL,
`output_plugin` still echoes `pgoutput` for logical slots.

## Test plan

Two unit tests in `internal/server/replication_test.go` cover the
new behaviour:

- `TestReplicationCreateLogicalSlotWithOptionsList` — the exact
  shape libpqwalreceiver sends: `CREATE_REPLICATION_SLOT "sub_paren"
  LOGICAL pgoutput (SNAPSHOT 'nothing')`. Asserts the four-column
  reply shape, NULL `snapshot_name`, `pgoutput` output_plugin, and
  that the backing store records a `SlotLogical` slot.
- `TestReplicationCreateLogicalSlotOptionsListMultiple` — exercises
  the comma-separated path (`(SNAPSHOT 'use', TWO_PHASE)`) and pins
  the syntax-error path for unknown options (`(FROBNITZ true)`),
  ensuring future probe rungs surface loudly rather than being
  silently absorbed.

The live interop test (`TestPort_PgoutputInteropGoopgToPG`) was
exercised manually to confirm the next rung; with rung 8 closed it
advances past `CREATE SUBSCRIPTION` and stalls in the apply path
(rung 9, deferred).

## Out of scope

- Snapshot exporter / `SNAPSHOT 'export'` actually returning a
  snapshot name. PG18 still accepts `SNAPSHOT 'nothing'`, which is
  what the live test sends; deferred until a probe genuinely requires
  the value.
- Two-phase commit decoding. The option name is acknowledged, but
  no two-phase frames are emitted by pgoutput in v0.
- Failover slots (PG17+). Acknowledged at parse time; no failover
  semantics are implemented.
- The apply-path stall observed once rung 8 was closed — that lives
  in its own rung 9 design doc.

## Future work

- Rung 9: subscriber apply stall after `CREATE SUBSCRIPTION`. Capture
  publisher + subscriber logs from a fresh interop run, identify
  which probe or stream message fails, land a targeted fix with its
  own design doc (`0103-0014`).
