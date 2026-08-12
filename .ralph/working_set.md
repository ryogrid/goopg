(idle — nothing in flight)

M0131-S21d LANDED (loop #169) — `heap_xlog_update` now acquires its pages the way
every other heap redo routine does, and **a real PG's crash tail replays END TO
END**: the S28 E2E starts, finishes recovery and writes goopg's own checkpoint
over the PG data directory. No replay refusal is left in that stream.

Files: `internal/wal/recovery.go` (`replayDecodedXLogHeapUpdate` — manual
NBlocks/ReadBlock/IsNew guards replaced), `internal/wal/heap_update_pg_test.go`
(2 new tests), design `docs/design/0131-0015-*` §"S21d" + Guard 9,
`.ralph/fix_plan.md` (S21d checked, S21e + S21f filed), 2 ledger rows.

The discovery worth keeping: the update record's two block refs are NOT
symmetric, and the asymmetry is upstream's. Block 0 (new version) is
`XLogInitBufferForRedo`/`XLogReadBufferForRedo` → `redoHeapPageForBlock`:
zero-extend past the flushed end, honour `XLOG_HEAP_INIT_PAGE`. Block 1 (old
version, cross-page only) is RBM_NORMAL → `redoExistingHeapPageForBlock`: an
absent page is `BLK_NOTFOUND`, skip the stamp, never extend. Only the page
receiving the new tuple may be created.

S28 gate stop MOVED OFF WAL entirely: it now fails at
`relation "s28_items" does not exist` (42P01) — goopg's boot does not build its
in-memory catalog from the on-disk pg_class/pg_attribute it just replayed. Filed
as **M0131-S21e** (ledger row). Sibling audit found three PG-format paths still
refusing where upstream skips (heap-delete, heap-prune, replayExistingXLogBlock)
— deliberately NOT folded in (they fail loudly, not silently) → **M0131-S21f**.

Gates: `internal/wal` + `internal/storage` PASS, `-race` on the touched wal tests
PASS, UNITS precommit PASS (warm cache), S28 E2E advanced (still red, new layer),
pgbench smoke via the commit hook, `make ralph-state-guard` OK (auto-repaired the
stale completed marker). Fail-when-broken proven by a scripted re-insertion of
the `block does not exist` guard → the new-page test FAILS.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY, nothing new.

Next loop (banner = M-NIGHTLY filing, then M0131): **M0131-S21e** — it is the S28
gate's current stop, though it is a catalog-cold-start task, not WAL. Cheaper
alternatives if that proves large: S21f (mechanical) or S23 (cheap tail).

In-flight: none.
