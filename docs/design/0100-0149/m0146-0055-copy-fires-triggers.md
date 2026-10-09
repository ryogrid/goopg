# M0146-0055 — COPY FROM fires INSERT triggers

Status: done 2026-10-06 (`bd4923c05`).

## Symptom

A BEFORE INSERT FOR EACH ROW trigger that sets `new.b := new.b || '!'`
changed nothing under COPY: goopg stored `x,y` where PG 18.3 stores `x!,y!`.
The probe against PG showed more:

- no statement-level or AFTER ROW trigger ran;
- a BEFORE ROW trigger returning NULL could not suppress a row (`COPY 3`;
  PG `COPY 2`);
- a `GENERATED ALWAYS AS (...) STORED` column was left NULL;
- a trigger's RAISE NOTICE during a stand-alone COPY never reached the
  client.

## PG behaviour (`src/backend/commands/copyfrom.c` `CopyFrom`)

1. `ExecBSInsertTriggers`: BEFORE STATEMENT, before the first row. It
   fires even for a COPY that stores no rows.
2. Per row:
   - `ExecBRInsertTriggers`: BEFORE ROW, which may replace or suppress the
     row (a suppressed row is not counted);
   - `ExecComputeStoredGenerated`;
   - `ExecConstraints`;
   - insert;
   - queue the AFTER ROW events.
3. At statement end (`AfterTriggerEndQuery`): the queued AFTER ROW
   triggers in row order, then `ExecASInsertTriggers` (AFTER STATEMENT).

Observed NOTICE order: `BEFORE statement`, `before row x`, `before row p`,
`after row x`, `after row p`, `AFTER statement`.

## Change (`internal/executor/copy.go`)

- `newCopyFromExecutor` records whether the table has BEFORE ROW, AFTER
  ROW or statement-level INSERT triggers.
- `beginStatement` (once) fires BEFORE STATEMENT. `storeCopyRow` calls it
  first, and `endStatement` calls it too, so a zero-row COPY fires it.
- `storeCopyRow`:
  1. defaults (`fillDefaults`, M0146-0054);
  2. BEFORE ROW via `fireTriggers` — a NULL return skips the row without
     counting it;
  3. `computeGeneratedColumns`;
  4. NOT NULL / CHECK / domain constraints;
  5. queue the row for AFTER ROW;
  6. write (single or multi-insert).
- `endStatement` (once) fires the queued AFTER ROW triggers, then AFTER
  STATEMENT. It is reached from:
  - `Finish` (text/CSV, file);
  - the binary trailer in `PushBinaryData`. No wire layer calls `Finish`
    on that path.
- `copyUsesMultiInsert` already forces single-row inserts for BEFORE ROW
  triggers. AFTER ROW events are queued in either mode and fire after the
  last flush.
- `internal/postmaster/copy.go`: the stand-alone COPY Context gets
  `NoticeFlush`, as dispatch wires it, so notices are sent when raised.

## Verification

- `TestCopyFromFiresTriggers` runs PG's probe script:
  - CSV, binary, zero-row and generated-column cases;
  - it checks the NOTICE order, the stored values, the suppressed row and
    the row count;
  - 10 assertions fail at HEAD.
- The server probe against PG on :5534 is byte-identical.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24 MATCH;
  - sf025 96/96. Its runtime moves were re-timed solo against a HEAD
    binary and are noise;
  - ea-ratchet PASS.
- Regress A/B over 8 files:
  - copy2 improved (BEFORE trigger values appear);
  - copydml: notices now arrive inline;
  - triggers: COPY's statement triggers now fire as in PG;
  - copy: the progress-report trigger now fires and fails on a PL/pgSQL
    gap (below);
  - generated_stored, insert and copyselect identical.

## Not covered (shared with INSERT unless noted)

- **Trigger WHEN conditions** (`WHEN (NEW.a = 123)`) are not evaluated by
  `fireTriggers`, so the trigger fires for every row (regress `triggers`).
- **Partitioned targets:** row triggers fire on the partitioned parent,
  not on the leaf partition each row is routed to, and a partition's own
  triggers do not fire. This is the trigger-cloning gap ledgered under
  M0134-0078.
- **Transition tables** (`REFERENCING NEW TABLE`) are not populated.
- **INSERT itself** fires AFTER ROW triggers inline per row (not queued
  to statement end) and never fires AFTER STATEMENT. COPY now does both;
  INSERT still does not.
- **`COPY (DML ... RETURNING) TO STDOUT`** emits the RETURNING rows after
  the DML completes, so its data follows both trigger notices. PG streams
  the row between the BEFORE and AFTER notices.
- **regress `copy` progress-report trigger:** it now fires, as in PG, but
  goopg's PL/pgSQL rejects `WITH ... SELECT INTO`, and
  `pg_stat_progress_copy` is not implemented.
