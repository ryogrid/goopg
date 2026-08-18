# Working set — M0134-0005ac LANDED (not-null verification scan coverage)

**Task:** M0134-0005 / sub-item **M0134-0005ac** — LANDED, item ticked. Selected
per the Current Priority banner (M-NIGHTLY had no new item; M0134 is next).

**Nightly triage:** `ci/logs/action-items.md` still shows run `20260819-011823`,
items: 1 — the SAME AI-20260819-011823-001 fixed three loops ago (`2289e149`).
Nothing new to file. The next nightly run should drop it.

**What landed:** one root cause, four symptoms — goopg's NOT-NULL/PK existing-row
verification either never ran or scanned storage that is empty by construction.
(B) `VALIDATE CONSTRAINT <notnull>` flipped `NotValid=false` with zero scan,
protected by an in-code comment that wrongly generalised `tablecmds.c:9956`'s
*ADD*-path exclusion over `ATExecValidateConstraint`'s own scan at `:13291-13295`.
(C) `forEachLiveRow` scanned only `RelFileNode(tbl)` — a partitioned parent has
no own storage, so `SET NOT NULL` / `ADD PRIMARY KEY` on a parent scanned nothing.
Fix: `forEachLiveRow` recurses via `allDescendants`+`snapDetachEpoch` (machinery
the FK validator already used), new `forEachLiveRowRel` scans the leaf and remaps
BY NAME into a parent-shaped buffer. Callback became `func(row Row, relName
string) error` at all 5 sites — forced by the oracle: PG's phase-3 error names the
relation HOLDING the row (`constraints.out:1254` `cnn_part1`, `:1561`
`notnull_tbl1_3`), not the ALTER target. The first pass got this wrong; the
regress runner caught it.

**Measurement:** constraints **406 → 381 lines, 21 → 19 hunks**. Never compare to
a pre-2026-08-19 number. One NEW `@@` header appeared and is an **unmasking**:
`VALIDATE CONSTRAINT nn3` used to succeed silently and accidentally left `nn3` in
the state PG's real cascade produces; now that it errors, the already-ledgered
`SET NOT NULL` descendant-`convalidated` gap shows at a second script location.
Judge by hunk count, not `@@` count.

**Worth not re-learning:** a wrong in-code comment can shield a bug longer than
missing code does — an exclusion in ONE PG path is not a statement about the
feature. And a fixed bug can expose a second one it was compensating for.

**Gates run:** `go build ./...` PASS; `go test ./internal/executor/
./internal/catalog/` PASS; `scripts/pg-regress-runner.sh --verbose constraints`
406/21 → 381/19; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS; the 5 guards re-run by the coordinator via `tester` PASS; pgbench smoke via
the hook.

**Next step:** continue M0134-0005 at the **381/19** baseline. The census at
`tmp/ralph-handoffs/m0134-0005ac-census/report.md` (21 hunks → 12 buckets A-L) is
still ~90% valid — subtract buckets B and C. Top remaining, per that ranking:
1. **bucket G** — 4 independent low-risk line-pinned display/property-copy fixes
   (ATTACH PARTITION FK `_1` renaming; `\d+` sequence default not schema-qualified;
   LIKE not copying PK deferrability; the `(inherited)` tag on a redeclared+
   inherited NOT NULL). ~61 lines / 4 hunks, but 4 causes — pick ONE.
2. **bucket E** — hunks 11+12, two missing NOT-NULL validations (NO-INHERIT
   conflict on a descendant; duplicate constraint name within one CREATE TABLE).
3. **bucket D** — `pg_get_partition_constraintdef`, 1 hunk but 22 lines and
   breaks `\d+` on ANY partition; from-scratch builtin, higher risk.
Standing ledgered: chained-cast column label (bucket A, needs PG strength
semantics), and the `convalidated` cascade now visible twice.

**Delegation:** `tmp/ralph-handoffs/m0134-0005ac-{census,impl}/`
(researcher `aa99631f9a3f8ddc7` DONE 1 round; implementer `a7e1fc3f81676c356`
DONE 1 round, one in-goal deviation: the `relName` callback parameter, without
which the error text could not match PG; tester `aa461df390d7d3bd8` re-verified
the 5 guards pre-commit).

**In-flight:** none.
