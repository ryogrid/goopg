Task: M0119-0006 residual — whole-database `pg_amcheck` on a `CREATE DATABASE`'d
  db failed (ledger row resolved via appended resolution record; per-db extension
  registry landed). COMMIT-BLOCKED by owner HOLD — see In-flight.
Files (ALL STAGED, index ready to commit):
  internal/catalog/catalog.go, internal/catalog/extension_perdb_test.go,
  internal/executor/operators_ddl.go, internal/executor/sys_pg_extension.go,
  internal/initdb/catalog_heap_reload.go, internal/initdb/open.go,
  internal/testport/pgamcheck004_port_test.go,
  docs/design/0100-0149/0119-0006bs-per-database-extension-registry.md,
  docs/design/README.md, .ralph/{fix_plan,deferral_ledger,working_set,progress}*
Key symbols: extensionRegistryKey (db+"\x00"+lcname), CreateExtension→(bool,error),
  ExtensionOID(db,name), DropExtension→(oid,scopeDB,found),
  extensionScopeHeapDBOid, reloadUserExtensionsFromHeapForDB.
Hypothesis/Findings: registry was name-keyed globally; reload was DEAD CODE nested
  in the pg_collation error branch after pool close. Both fixed + verified live on
  scratch :5533 (cluster stopped; data kept at tmp/l29-amcheck/data).
Next step: when `bench/tpch/runtime_goopg/data.HOLD` disappears, re-run
  `scripts/tpch-spotcheck.sh` + `scripts/tpch-acceptance-arm.sh on
  tmp/arm-on.txt` (stamps must match staged code_tree 6b0af670…) then `git
  commit` the STAGED index — body needs `PARITY: N/A — catalog/DDL-registry
  change; no plan paths` + subject `catalog(extensions): per-db registry +
  heap reload — pg_amcheck on created dbs (M0119-0006bs)`. Do NOT `git add`
  more; index is exact. If internal/ changes, re-run all 3 gates.
Gates run: units PASS; tpcds-sf025 PASS=96/0-mismatch/0-shape-change (stamp
  matches staged code_tree); tpch-spotcheck SKIP-BLOCKED (HOLD);
  tpch-acceptance-arm not run (same HOLD — would SKIP; stamp file removed
  after accidental bare-invocation FAIL). Live pg_amcheck -d amcheckdb +
  --heapallindexed exit 0; restart survival + scoped DROP verified.
In-flight: STAGED COMMIT waiting on owner — `bench/tpch/runtime_goopg/data.HOLD`
  (OWNER-PLANNED-RELOAD 2026-09-20, M0142-0003i 8-FK rebuild; hammerdbcli was in
  "CREATING TPCH INDEXES" at ~06:07, log /tmp/tpch-reload-build.log). Owner lifts
  HOLD after their spotcheck. No gate-exception row exists; loop cannot add one.
