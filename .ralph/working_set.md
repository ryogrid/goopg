# Working Set — loop end 2026-10-31 (COPY pgstat-counts impl loop)

- Task: M\-NIGHTLY "COPY skips the pgstat row counters" \(Kind: impl\, banner item 10 first open in file order\)\.
  CLOSED: COPY FROM now emits recordRelInsert at both heap\-write arms; code `9d0db8766`, records `6a67b564b`, pushed\.
- Files: `internal/executor/copy\.go` \(flushBatch emits len\(batch\) per multi flush per heapam\.c:2702; CIM\_SINGLE arm
  emits 1 per row per heapam\.c:2251; suppressed rows uncounted\)\, `internal/postmaster/copy\.go` \(ectx\.AdvisorySessionIdentity
  \= sess — dispatch twin; without it COPY counters sat at sessionStatsID 0, unreachable by the session's flush/getters\)\,
  `internal/executor/copy\_pgstat\_test\.go`, `internal/testport/copy\_pgstat\_server\_test\.go`,
  `internal/testutil/cluster/cluster\.go` \(DSN\(\) accessor\)\, `.ralph/fix\_plan\.md`, `\.ralph/deferral\_ledger\.md`,
  design doc "Follow\-up 3" \+ README row `m-nightly-copy-pgstat-counts`\.
- Key symbols: `storeCopyRow`/`flushBatch`/`copyUsesMultiInsert`, `recordRelInsert`→`relStats` \(staging→pending→shared
  \+ direct trigger bump\)\, `TriggerSnapshot`, `sessionStatsID`, `relStats\.flush` \(pg\_stat\_force\_next\_flush twin\)\,
  `applyXactToPending` commit/abort math; lib/pq CopyIn lives on the driver\.Stmt path \(tx\.Prepare\(pq\.CopyIn\(\.\.\.\)\)
  \+ row Execs \+ Close\)\, not Exec/QueryContext — QueryContext fails "got N parameters" via stmt\.exec\.
- Hypothesis/Findings: view cells `n\_tup\_ins`/`n\_live\_tup` stay honest\-0/reltuples renderers for ALL DML \(M0122\-0003
  design — ledger'd; the getter layer PG's view builds on is now exact for COPY: 5000 on all four getters post\-flush\)\;
  no statement\-end flush \(pending→shared only via explicit flush — ledger'd\)\; COPY\-into\-partitioned\-parent counts the
  parent \(ledger'd\)\; postmaster full\-suite flakes TestSimpleQueryBatchAbortUndoesEarlier\* reproduce IDENTICALLY at HEAD
  \(ledger M0122\-0014 2026\-10\-30 row\)\; TestExplainSplitAggregateChainCosts flaked once, passed in the gated full run\.
- Next step: banner item 10, M\-NIGHTLY next in file order — "pgbench\_branches reltuples renders 1" \(Kind: recon\)\:
  pure\-SQL repro COPY 10 rows → UPDATE 9 rows → ANALYZE → read reltuples; test the COPY\-committed\-tuples\-invisible\-to\-
  later\-snapshots hypothesis \(clog/commit\-ts lag vs launcher snapshot minting\)\; rigs `tmp/mnightly\-trio\-rig\.sh`
  \(SCALE=1\; self\-builds bin/goopg\) \+ `tmp/mnightly\-copy\-probe\.sh`\.
- Top-residual: n/a — M0146\-0014 census exhausted at routing\-20261031b \(REUSE at `6a67b564b`\); M\-NIGHTLY has no census\.
- Skip-rationale: selected item 10 \(M\-NIGHTLY impl\)\; items 0\-9 open `\ [ ]\` tokens: M0146\-0014\(channel\),
  M0146\-0042\(channel\), M0146\-0007/0153/0154/0160\-0166/0167/0168/0173/0205/0238/0239/0243\(below\-item\-10\),
  M0142\-0008c\-1a/3d/4\(blocked\-on M0145\-0003\), M0146\-0010a/0035\(owner\-held\), P0\-E9..E16\(owner\-placement\),
  M0147\-0000\(channel\), M0147\-0023/0048\(owner\-held\), M0122\-0013\(owner\-held\), M0119 rows\(below\-item\-10\),
  M0122\-0009a/b/c/0010a\(below\-item\-10\)\. Banner "P0\-E8 OPEN" is a stale label — the task is `\ [x]\` DONE, not selectable\.
- Gates run: units\-scope ralph\-precommit rc=0; tpch\-spotcheck Q12=2/Q13=35 PASS; acceptance arm 24 MATCH vs
  tmp/arm\-off\-20261030\.txt; tpcds\-sf025 sweep same=99 changed=0; testport server pin PASS \(5000 on all four getters\);
  go test ./internal/executor full ok; postmaster full = only ledger'd pre\-existing flakes \(HEAD reproduces identically\);
  commit\-hook pgbench smoke PASS ×2; census REUSE post\-record\.
- In-flight: none\. HEAD `6a67b564b` pushed\. bin/goopg may not exist \(rebuild: `go build -o bin/goopg ./cmd/goopg`\;
  rigs self\-build\)\. Rigs need `GOMEMLIMIT=1500MiB` when GOOPG\_MEM\_HIGH is small\.
