# Working Set — loop end 2026-10-31 (0-byte relfilenode truncate loop)

- Task: M\-NIGHTLY \(AI\-20261031\-002109\-005 follow\-up\) — "DROP TABLE IF EXISTS of a missing relation routes
  through the TRUNCATE executor and creates 0\-byte relfilenode files", banner item 10\. CLOSED: filed routing
  verified NOT reproducing at HEAD; two real 0\-byte mechanisms landed\. Code `dbd865599`, records `18ab82f04`, pushed\.
- Files: `internal/storage/smgr\.go` \(relFileForTruncate: no O\_CREATE/MkdirAll, missing file → no\-op per md\.c:1291\),
  `internal/executor/operators_ddl\.go` \(truncateTableAndPartitions skips PartitionKey != nil parents per
  tablecmds\.c:2148\-2150\), `internal/storage/smgr_truncate_nocreate_test\.go`,
  `internal/executor/drop_truncate_nostorage_test\.go`, `internal/testport/drop_if_exists_no_storage_test\.go`,
  `docs/design/0100\-0149/m\-nightly\-truncate\-eviction\-flush\-race\.md` "Follow\-up" section \+ README row \(with code\),
  `.ralph/fix_plan\.md` \(closure\), `.ralph/deferral_ledger\.md` \(1 row, items a–d\)\.
- Key symbols: `Manager.relFileForTruncate` / `TruncateRelation` / `TruncateRelationTo`,
  `truncateTableAndPartitions`, `execDropTable` \(NOTICE arm verified clean — no OID burn, no file\),
  `Manager.relFile` \(read/write O\_CREATE policy kept\)\.
- Hypothesis/Findings: the DROP\-IF\-EXISTS\-via\-TRUNCATE stack trace from the diagnosis does not reproduce
  \(fixture \+ fresh\-cluster probes\)\. S9 census after the record returns **REUSE** at routing\-20261031b —
  correct: trigger set covers only vm\.go/bufpool\.go under internal/storage and \*explain\*/\*analyze\*/\*stat\*
  under internal/executor, none staged; capture ≤24 h, so no census work needed\. Ledger deferrals: parent
  relfilenode=0/no\-file at CREATE; VACUUM FULL/CLUSTER sibling truncate sites \(operators_ddl\.go ~21373,
  ~27745\-27763, ~28067\); read\-path relFile O\_CREATE policy; TRUNCATE ONLY partitioned SQLSTATE
  0A000 vs PG 42809 \(operators_ddl\.go:17083\)\.
- Next step: banner item 10, M\-NIGHTLY next in file order — "autovacuum ANALYZE reports relpages far above
  the relation's block count" \(filed 2026\-10\-31, Kind: **recon** — commits referencing it may stage no
  non\-test internal/cmd files; pages=132 for a 1\-block pgbench\_branches file\)\.
- Top-residual: n/a — M0146\-0014 census exhausted at `c8de1fe4c` \(routing\-20261031b, REUSE at `18ab82f04\`:
  CATEGORIES\-EXCL\-MATCH 0 everywhere\); M\-NIGHTLY has no census\.
- Skip-rationale: selected item 10 \(M\-NIGHTLY\); items 0\-9 open `\ [ ]\` tokens: M0146\-0014\(channel\),
  M0146\-0042\(channel\), M0146\-0007/0153/0154/0160\-0166/0167/0168/0173/0205/0238/0239/0243\(below\-item\-10\),
  M0142\-0008c\-1a/3d/4\(blocked\-on M0145\-0003\), M0146\-0010a/0035\(owner\-held\), P0\-E9..E16\(owner\-placement\),
  M0147\-0000\(channel\), M0147\-0023/0048\(owner\-held\), M0122\-0013\(owner\-held\), M0119 rows\(below\-item\-10\),
  M0122\-0009a/b/c/0010a\(below\-item\-10\)\. Banner "P0\-E8 OPEN" is a stale label — the task is `\ [x]\` DONE, not selectable\.
- Gates run: go test storage \+ executor full PASS; testport drop pin PASS; units \(precommit scope=units\) PASS
  \(warm cache, no \-count=1\); tpch\-spotcheck Q12=2 Q13=35 PASS; tpch\-acceptance\-arm 24 MATCH PASS vs
  tmp/arm\-off\-20261030\.txt; tpcds\-sf025 sweep PASS \(99 compared, PLAN\-SHAPE same=99 changed=0, non\-blocking\);
  fireset not required \(no optimizer/planner staged\); census REUSE post\-record; commit\-hook pgbench smoke PASS
  \(both commits\)\. Race gate not run \(no concurrency change\)\.
- In-flight: none\. HEAD `18ab82f04` pushed\.
