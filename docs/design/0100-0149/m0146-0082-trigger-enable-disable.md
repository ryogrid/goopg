# M0146-0082 — ALTER TABLE ENABLE/DISABLE TRIGGER

Status: done 2026-10-07 (`e3cba3b96`).

## Symptom

From regress `triggers.sql` ("Test enable/disable triggers"):

```sql
alter table trigtest disable trigger trigtest_b_row_tg;
insert into trigtest default values;      -- goopg still fired BEFORE ROW
set session_replication_role = replica;   -- goopg: unrecognized parameter
delete from trigtest where i=2;           -- goopg: DELETE 0, no cascade
```

The statement only took its ShareRowExclusiveLock; nothing else changed:
- every disabled trigger kept firing;
- `pg_trigger.tgenabled` was always `O`.

The DELETE failed because the disabled BEFORE ROW trigger returns `NEW`,
which is NULL for a DELETE. That suppressed the row, so the
`ON DELETE CASCADE` never ran.

## PG

- `EnableDisableTrigger` (trigger.c) sets `tgenabled` on the named
  trigger. USER sets it on every non-internal trigger, and ALL on every
  trigger including the internal RI triggers (a superuser check applies).
  An unknown name raises 42704 `trigger "x" for table "t" does not exist`.
  Row triggers recurse into a partitioned table's partitions unless ONLY
  is given.
- `TriggerEnabled` (trigger.c) decides whether a trigger fires, against
  `session_replication_role` (guc_tables.c; PGC_SUSET;
  origin/replica/local, default origin):
  - `D` never fires;
  - `O` fires in origin and local;
  - `R` fires only in replica;
  - `A` always fires.
- Foreign keys are enforced by RI triggers:
  - `RI_FKey_check_*` on the referencing table;
  - the action triggers (`RI_FKey_*_del` / `_upd`) on the referenced
    table.

  So ALL also turns off FK checks and cascades. These RI triggers are
  `O`, so the replica role skips them too.

## Change

- **Grammar.**
  - `trigger_toggle` is `<strs>` {mode, kind, name}. `opt_trigger_mode`
    gives `O`, `A` or `R`, and DISABLE gives `D`.
  - `trigger_target` is {name, x}, {all}, or {user}.
  - The values land on `AlterTableStmt.TriggerFireMode`,
    `TriggerTargetKind` and `TriggerName`.
  - The playbook §12 was read first; `make gen-parser` added no
    conflicts.
  - The golden diff only adds the three fields, and the four
    trigger-toggle entries carry the right values.
- **Catalog.**
  - `Trigger.Enabled` is `tgenabled`; its zero value reads as `O`
    (`FireMode`, `TriggerFireMode`). `pg_trigger` emits it.
  - `ForeignKey.CheckTrigEnabled` and `ActionTrigEnabled` stand in for the
    RI triggers' `tgenabled`, since goopg enforces foreign keys without
    triggers.
- **GUC.** `session_replication_role` is registered (enum, PGC_SUSET) and
  added to `postgresql.conf.sample`.
- **Firing.** `triggerModeFires` implements the TriggerEnabled test.
  - `fireTriggersCols` and `fireStatementTriggersCols` skip a trigger that
    does not fire. These two cover BEFORE triggers, queued AFTER triggers
    and statement triggers.
  - `checkFKInsertForConstraints` applies the test to `CheckTrigEnabled`.
  - `enforceFKOnDelete` and the partition ancestor pass apply it to
    `ActionTrigEnabled`.
- **`enableDisableTrigger`.**
  - name: sets that trigger, or raises 42704.
  - USER: sets every trigger.
  - ALL: sets every trigger, plus the stand-ins on the FKs this table
    owns and the FKs that reference it.

## Verification

- `TestAlterTableEnableDisableTrigger` follows the regress section step by
  step:
  - notices after each ALTER;
  - replica role and ENABLE ALWAYS;
  - `tgenabled`;
  - the cascade, and no cascade after ALL;
  - the unknown-name error.

  Every want is PG 18.3's.
- A server probe of the regress section matches PG through the inheritance
  part. The partitioned-table part differs only where the trigger clones
  are missing.
- Regress A/B over 6 files on fresh clusters:
  - triggers 2570 → 2471, rules −2, event_trigger −2;
  - foreign_key, alter_table and plpgsql are identical;
  - the only new lines are the 42704s for `aft_row` on partitions.
    goopg never cloned the trigger onto them (the held partition-clone
    finding).
- Gates: units, tpch-spotcheck, arm 24/24, sf025 96/96 (plan shapes
  99/99), ea-ratchet PASS.

## Not covered

Held in the M0146-0055 escalation. The lineage guard refused them as new
descendants of that root; each has a ledger row:

- **Partition trigger clones.** Row triggers on a partitioned table are
  not cloned onto its partitions, so:
  - they do not fire for routed rows;
  - `pg_trigger` has no child rows;
  - ALTER cannot recurse to the partitions.
- **`regclass IN (SELECT oid …)`** returns no rows.
- **Trigger durability.** Triggers are not durable across a restart. The
  trigger catalog state is not transactional either: an ALTER … DISABLE
  TRIGGER survives ROLLBACK.

Ledgered:

- The RI triggers are absent from `pg_trigger`; their state lives on the
  FK stand-ins.
- ALL does not check superuser for internal triggers.
- A queued AFTER trigger is checked at fire time, not at queue time
  (AfterTriggerSaveEvent). They differ only if the state changes inside
  the statement.
