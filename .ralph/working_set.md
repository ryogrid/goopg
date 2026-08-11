(idle — nothing in flight)

M0131-S21a-2 PART 6 LANDED (loop #161) — `SMGR_TRUNCATE` (RM_SMGR, 0x20).
Committed + pushed to `make-db-cluster-compat` (1bc53c79).

Files: `internal/wal/{pg_xlog_decode.go,recovery.go}`,
new `internal/wal/smgr_truncate_pg_test.go`, `internal/storage/smgr.go`,
design `docs/design/0131-0015-pg-wal-opcode-coverage.md` §"S21a-2 … part 6"
(+ opcode-matrix row + `docs/design/README.md` index line +
`.ralph/fix_plan.md` S21 note update).

Key symbols: `decodeXLogSmgrTruncate`, `applySmgrTruncate`,
`storage.Manager.TruncateRelationTo` / `relFile.truncateTo` (new partial-
truncate primitive; existing `TruncateRelation`/`truncateToZero` only ever
went to 0 blocks).

What landed:
- goopg's native `RecordKindSmgrTruncate`/`replaySmgrTruncate` always drops
  a relfile to 0 blocks and its decoder carries only `{dbOid,relOid,fork}`
  — no `blkno`, no fork bitmask. A real PG VACUUM tail truncation is
  genuinely partial (main fork to a non-zero surviving prefix) plus an
  independent vm/fsm-fork zero-out, which those primitives can't express.
- `decodeXLogSmgrTruncate` parses `xl_smgr_truncate` (BlockNumber blkno +
  RelFileLocator + int flags, 20 bytes), reusing `decodeXLogSmgrCreate`'s
  default/global tablespace-OID → TblOid=0 remap.
- `applySmgrTruncate` mirrors `smgr_redo`'s exact order: `applySmgrCreate`s
  the main fork first (upstream "prefer to recreate the rel … until the
  drop is seen"), then truncates main to `blkno` under
  `SMGR_TRUNCATE_HEAP`, vm to 0 under `_VM`, fsm to 0 under `_FSM`, each
  flag independently gated.
- Deliberate deviation, no ledger row: upstream's pre-truncate
  `XLogFlush(lsn)` is a live-server torn-truncate durability guard;
  goopg's single-threaded startup replay pass has no counterpart need.
- 4 guards (`internal/wal/smgr_truncate_pg_test.go`): partial main-fork
  truncate (10→3 blocks, idempotent replay); VM-only flag leaves
  main/fsm untouched; a truncate naming a relation with no on-disk main
  fork recreates then truncates it (matches upstream's create-then-drop,
  net 0 blocks when blkno=0); tablespace-OID remap round-trip. ALL proven
  fail-when-broken by a scripted revert of the dispatch case (commented
  out, confirmed all 3 dispatch-dependent tests fail with "unsupported
  xlog record rmid=2 info=0x20", restored, confirmed pass).

Gates: `internal/wal` PASS + `-race` PASS, `internal/storage` PASS, UNITS
precommit PASS (`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`),
pgbench smoke PASS via the commit hook (SCOPE=smoke on commit).
`make ralph-state-guard` PASS (self-repaired a stale progress.json marker
from the prior loop's clean exit, as usual).

Nightly triage: `ci/logs/action-items.md` still on run `20260812-005501`
(unchanged from loop #160 — no new nightly run landed); all 4 `## AI-`
items already filed under M-NIGHTLY, nothing new to file this loop.

Next loop (banner = M-NIGHTLY filing, then M0131): **`HEAP2_REWRITE`'s
loud refusal** (0x00 RM_HEAP2, out-of-scope VACUUM-FULL/CLUSTER-with-a-
logical-slot — needs an `ErrUnsupportedRecord`-style message naming the
feature, not real redo; goopg has no `pg_logical/mappings` consumer).
That closes S21a-2 entirely. Then **S21b** (btree, ~6 opcodes —
INSERT_UPPER 0x10, INSERT_META 0x20, INSERT_POST 0x50, DEDUP 0x60,
DELETE 0x70, META_CLEANUP 0xE0; REUSE_PAGE 0xD0 needs no redo, already
recognised in S21a-1), gated on S16 which is already done. Each landing
shrinks S28's self-arming skip further.

In-flight: none.
