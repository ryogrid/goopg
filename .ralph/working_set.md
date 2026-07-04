(idle — nothing in flight)

Last completed (loop #107, 2026-07-04): closed DU-002 slice 439's own
resume point (2) — `ALTER TABLE ... OWNER TO` (and the `ALTER SEQUENCE`/
`ALTER VIEW` forms sharing its `execAlterTable` code path,
`internal/executor/operators_ddl.go`) now raises `role "..." does not
exist` (42704) for an unknown role instead of silently accepting it,
mirroring the `im.RoleOID` existence check every sibling OWNER TO site
(schema/statistics/collation/aggregate/publication/subscription/event-
trigger) already made. Fixed two pre-existing tests
(`TestAlterSequenceOwnerTo`/`TestAlterViewOwnerTo`) that relied on the old
unchecked behavior with an unregistered role name. New
`internal/executor/operators_alter_table_owner_test.go` (3 tests). Also
confirmed the sibling resume point (1) from the same original ledger row
(ALTER INDEX/VIEW/MATERIALIZED VIEW command-tag mistagging) was already
independently fixed — no change needed there. Design doc
`docs/design/0110-0001-pg-dump-tap-port.md` new "Follow-up" section;
ledger row appended (resolved). Corrected a stale memory file
(`goopg_grant_acl_virtual_vs_heap_blocker.md` / MEMORY.md) that still
claimed M0119-0004-ACLHEAP's typacl/attacl/datacl GRANT was blocked — it
was already `[x]` complete in fix_plan.md; verified via grep
(`AttrACLChange`/`DatabaseACLChange`/`execAttrACLChange` all exist).
Committed and pushed.

Next step: root-0024 is fully closed (confirmed this loop). Per fix_plan's
Current Priority banner, continue the M0119-0004 pg_dump catalog-view
parity battery, or pick the next unresolved DU-002 slice from the
deferral ledger. A triage of the ledger's 154 open (`status: -`) rows this
loop found many are stale/already-resolved without their status being
flipped (this ledger's own known imperfection — see e.g. row ~251 typacl/
attacl/datacl, already landed but still marked "-"). Shortlisted but NOT
yet done, ranked by the triage:
  1. `CREATE/ALTER ROLE` attributes beyond login/password/superuser
     (CREATEDB/CREATEROLE/BYPASSRLS/CONNECTION LIMIT/VALID UNTIL) are
     accept-and-ignore — `internal/server/role_ddl.go`
     `applyRoleAttrOptions` (~line 260) + `catalog.RoleAttrs`
     (`internal/catalog/catalog.go:11517`, only has CanLogin/Superuser/
     CredType/Secret).
  2. View `WITH CHECK OPTION` is captured for pg_dump fidelity but never
     enforced at runtime (no `23514 new row violates check option for
     view` anywhere — grepped, zero hits outside the ledger) —
     `internal/parser/ddl.go` `parseCreateViewTail` WITH(...) loop
     (~line 2382-2399) sets `CheckOption`; needs INSERT/UPDATE-through-
     view enforcement in the executor.
  3. FDW `HANDLER`/`VALIDATOR` function references parsed and discarded
     (`internal/parser/ddl.go:464`) — likely entangled with a general
     regproc-OID-resolver infra gap flagged elsewhere; rank last.
Before picking one, re-verify it's still open (many "open" ledger rows
turned out already fixed this loop) and check for the smallest blast
radius first (item 1 looks like a similarly-small template-following
fix, similar in shape to this loop's OWNER TO fix).

Gates run this loop: go build ./... clean; go vet ./internal/executor/...
clean; go test ./internal/executor/... PASS (full, incl. `-race`); go test
./internal/server/... PASS; scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33);
pgbench smoke = pre-commit hook; make ralph-state-guard OK (self-repaired
a stale progress.json "completed" marker again — same recurring artifact
noted in the last several loops' carries, seems to be a benign side effect
of how the previous loop's clean exit writes progress.json).
