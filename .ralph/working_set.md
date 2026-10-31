# Working Set — loop end 2026-10-31 (relpages-inflation recon loop)

- Task: M\-NIGHTLY \(AI\-20261031\-002109\-005\) — "autovacuum ANALYZE reports relpages far above the relation's
  block count" \(Kind: recon\)\. CLOSED as **not\-a\-defect**: instrumented pgbench\-trio repros show every ANALYZE
  pages == manager\-cached nblocks == on\-disk file blocks at sample time; the nightly "132 for a 1\-block file"
  is sample\-time bloat vs the post\-`-S`\-init `TRUNCATE pgbench_history` file state \(RECON\-TRUNC\-ZERO
  cachedBefore=4003\), and TRUNCATE zeroes TableStats \(operators\_ddl\.go:17179; PG trunc\.c:282\-285 parity\)\.
  Records commit `7d14c5358`, pushed\. No code staged \(recon\)\.
- Files: `.ralph/fix_plan\.md` \(closure \+ 3 filed findings\)\, `docs/design/0100\-0149/m\-nightly\-truncate\-
  eviction\-flush\-race\.md` "Follow\-up 2" \+ README row\. Repro rigs \(tmp, uncommitted\): `tmp/mnightly\-
  \{relpages,trio,trunc,copy\}\-rig\.sh`\.
- Key symbols: `analyzeRelationWith` \(pool\.NBlocks==file at every print\)\, `vacuumCore` \(Stats\.Pages = VISITED
  pages; VM\-skips uncounted — explains "vacuum pages=1479 vs analyze 2572"\; lastNonEmpty/VM\-skip parity
  verified PG\-shaped\)\, `Pool.TruncateRelationTail`→`Manager.TruncateRelationTo` \(handle nblocks updated\)\,
  execTruncate `tbl.Stats = nil` \(operators\_ddl\.go:17179\)\, `recordRelInsert` \(COPY never calls it\)\.
- Hypothesis/Findings: relpages inflation \(ruled out\)\; COPY pgstat gap \(PROVEN: goopg n\_tup\_ins=0/n\_live\_tup=0
  vs PG 5000/5000 after 5000\-row \copy\)\; pgbench\_branches reltuples=1 on idle 10\-row table \(PROVEN symptom;
  COPY/rewrite\-then\-update visibility family — COPY\-committed tuples judged invisible to later snapshots is
  the leading hypothesis: the COPY probe's n\_live\_tup=0 is consistent\)\; post\-TRUNCATE cost goopg 22\.70/1270
  vs PG 16\.10/610 \+ reltuples 0 vs \-1 sentinel\. S9 census REUSE \(routing\-20261031b\), twice verified\.
- Next step: banner item 10, M\-NIGHTLY next in file order — "COPY skips the pgstat row counters" \(Kind: impl\)\:
  call recordRelInsert\(ctx, oid, n\) with the loaded row count in the COPY executor path \(PG copy\.c
  pgstat\_report\_copy\)\; gate = pg\_stat probe vs PG \+ units\; keep it bounded to the emit site\.
- Top-residual: n/a — M0146\-0014 census exhausted at routing\-20261031b \(REUSE at `7d14c5358`\); M\-NIGHTLY has no census\.
- Skip-rationale: selected item 10 \(M\-NIGHTLY recon\)\; items 0\-9 open `\ [ ]\` tokens: M0146\-0014\(channel\),
  M0146\-0042\(channel\), M0146\-0007/0153/0154/0160\-0166/0167/0168/0173/0205/0238/0239/0243\(below\-item\-10\),
  M0142\-0008c\-1a/3d/4\(blocked\-on M0145\-0003\), M0146\-0010a/0035\(owner\-held\), P0\-E9..E16\(owner\-placement\),
  M0147\-0000\(channel\), M0147\-0023/0048\(owner\-held\), M0122\-0013\(owner\-held\), M0119 rows\(below\-item\-10\),
  M0122\-0009a/b/c/0010a\(below\-item\-10\)\. Banner "P0\-E8 OPEN" is a stale label — the task is `\ [x]\` DONE, not selectable\.
- Gates run: repro rigs only \(mini trio s=1 60s×3 with instrumentation; trio s=50 120s×3; TRUNCATE cost probe
  vs PG temp cluster; COPY probe vs PG temp cluster\)\; instrumentation fully reverted \(git checkout — tree clean
  of internal/ changes before commit\)\; commit\-hook pgbench smoke PASS; census REUSE post\-record;
  state\-guard OK\. No code gates needed \(no code staged\)\.
- In-flight: none\. HEAD `7d14c5358` pushed\. Note: `bin/goopg` was deleted \(rm for cleanup\) — rebuild with
  `make build`/\`go build \-o bin/goopg \./cmd/goopg\` before the rigs\; rigs now build it themselves\.
