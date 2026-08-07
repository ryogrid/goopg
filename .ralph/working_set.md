Task: pg_stat_activity empty PID fix — RESOLVED. `numericPIDOrNull` now
  returns `catalog.VirtualNull` instead of "" for empty/non-numeric PIDs.
  Also fixed `leader_pid` (always NULL, was "").

Files:
  - internal/initdb/pg_stat_activity_view.go: two-line fix (numericPIDOrNull
    returns VirtualNull; leader_pid uses VirtualNull)

Key symbols: numericPIDOrNull, registerPgStatActivityView, catalog.VirtualNull,
  planner.TypedVirtualCell

Hypothesis/Findings:
  - Root cause: `numericPIDOrNull` returned "" for non-numeric PIDs. "" fails
    strconv.ParseInt in TypedVirtualCell's int4 branch and falls through to
    StringConst{Value:""}. This emits '' on the wire instead of NULL, and the
    Go pq driver rejects the whole result set (strconv.ParseInt: parsing "").
  - Fix: return catalog.VirtualNull (the NULL sentinel) instead of "".
  - Also applies to pg_stat_ssl / pg_stat_gssapi (same numericPIDOrNull).
  - All 42 packages pass (ralph-precommit-test.sh units scope).

Next step: Pick next M-NIGHTLY item. Leading candidates:
  (a) pageHasSpaceFor vs insertItemSorted disagreement (btree correctness,
      well-diagnosed, repro shape known)
  (b) Merge m018 to master (all milestone work is landed, clean checkpoint)
  (c) M0123 pg_node_tree serialization (branch wal-pg-nodetree, major infra)

Gates run: ralph-precommit-test.sh (units) PASS, go build PASS,
  initdb/activity/catalog tests PASS, ralph-state-guard PASS (auto-repaired)

In-flight: none
