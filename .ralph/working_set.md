Task: M0119-0004 — per-DB publication scoping COMPLETE; subscription scoping next

Files:
- internal/catalog/pubsub.go: Added DBOid to Publication, compound map key,
  variadic dbOid on CreatePublicationAsOwner/LookupPublication/DropPublication/
  SetPublicationOwner, PublicationsForDBOid, recovery methods updated
- internal/catalog/pubsub_dbscope_test.go: NEW — cross-DB isolation test (PASS)
- internal/executor/context.go: Added PgPublicationRows field
- internal/executor/operators.go: pg_publication branch for per-connection override
- internal/executor/operators_ddl.go: Pass dbOid through execCreate/Drop/Alter
- internal/executor/operators_pg_get_publication_tables.go: PublicationsForDBOid
- internal/server/dispatch.go: Wire PgPublicationRows + publicationRowsForDBOid helper
- internal/server/logicalwalsender.go: Pass DefaultDBOid (walsender needs DB-aware follow-up)
- internal/initdb/catalog_heap_reload.go: Set DBOid = cat.DBOID() on reload

Key symbols:
- pubMapKey(dbOid, name) — compound map key (pubsub.go)
- PublicationsForDBOid(dbOid) — filtered iteration
- publicationRowsForDBOid(ps, dbOid) — VirtualRows builder (dispatch.go)
- PgPublicationRows func() [][]string — per-connection override (context.go)

Hypothesis/Findings:
- Publication per-DB scoping FIXED — round-trip error moved from "publication
  already exists" to "subscription already exists" (the next DU-002 blocker)
- Compound map key `pubMapKey(dbOid, name)` solves the same-name collision
- Subscription struct already has DBOid field (set at CREATE time), but
  CreateSubscriptionAsOwner/DropSubscription/LookupSubscription/Subscriptions
  don't filter by dbOid or use a compound key
- Pattern established by publications applies directly to subscriptions

Next step:
1. Add compound map key for subscriptions (subMapKey)
2. Update CreateSubscriptionAsOwner/DropSubscription/LookupSubscription/
   Subscriptions/SubscriptionsForDBOid to use compound key + filter
3. Add PgSubscriptionRows to Context + operators.go branch + dispatch.go wiring
4. Update replication_views.go VirtualRows + walsender
5. Add cross-database subscription isolation test
6. Verify: go build && go test ./internal/catalog/... ./internal/executor/...
   && TestPort_PgDumpConnectionSetup

Gates run:
- go build ./...: OK
- go test (catalog/executor/server/initdb): PASS
- TestCreatePublicationCrossDatabaseIsolation: PASS
- ralph-state-guard: REPAIRED + PASS
- pre-commit pgbench smoke: PASS (0 failed)

In-flight: none
