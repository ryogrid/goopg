Task: M0110-0003 (pg_amcheck 002_nonesuch) — loop #18. PORTED the final two
`--exclude-schema` sections of 002_nonesuch.pl, unblocked by loop #17's residual-#2
planner fix. COMPLETE + committed.

=== WHAT LANDED (this loop) ===
The two `--all --exclude-schema …` cases (upstream .pl :377-418) — whose exclude-CTE
anti-join PANICKED the backend before commit 36a085dc — now run end-to-end (exit 1,
`no relations to check`). They are the e2e regression guard for that planner fix.
Pure faithful test transcription; zero engine change.

Files:
- internal/testport/pgamcheck002_port_test.go (replaced the deferred residual-#2
  block with the two ported checkAmcheck cases; updated file header)
- docs/test-port/postgres-oracle-port-status.csv (AC-002 rationale: exclude-schema
  ported, only datconnlimit=-2 residual remains) + regenerated .md
- docs/design/0110-0003-pg-amcheck-tap-port.md (residual #2 → "ported" section;
  now "One deferred residual")
- .ralph/fix_plan.md (002_nonesuch row → PORTED)
- .ralph/deferral_ledger.md (loop #18 entry)

Gates: vet/gofmt/build clean. TestPort_PgAmcheck002Nonesuch PASS (1.9s). Full
pg_amcheck port suite PASS (21.5s). state-guard self-repaired OK. TPC-H spotcheck
N/A (test-only loop, no executor/planner/codec change).

=== NEXT STEP (resume) ===
002_nonesuch is fully ported except residual #1 (datconnlimit=-2 invalid-database
filter), which is BLOCKED on a runtime pg_database shared-catalog write goopg lacks
(goopg_no_runtime_shared_catalog_inplace_update) — a separate capability/milestone.
Other open M0110-0003 work: AC-003 remaining 003_check tiers (hash/gist/gin/brin/
spgist AMs, box/int4range/int4[] types, STORAGE EXTERNAL TOAST, multi-DB orchestration)
+ 005_opclass_damage. Other milestones: M0095-0003 recvlogical (030, logical decoding,
large); M0110-0001 pg_dump 002; M0110-0002 pg_waldump 002; M0117-0006/7/8 (Effort-L, defer).
