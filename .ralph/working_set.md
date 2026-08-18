# Working set — M0134-0005ad LANDED (two skipped NOT-NULL constraint checks)

**Task:** M0134-0005 / sub-item **M0134-0005ad** — LANDED, item ticked. Selected
per the Current Priority banner (M-NIGHTLY had nothing new; M0134 is next).

**Nightly triage:** `ci/logs/action-items.md` STILL shows run `20260819-011823`,
items: 1 — the same AI-20260819-011823-001 fixed four loops ago (`2289e149`).
Nothing to file. It has now been stale for four loops; if the next nightly run
does not refresh it, suspect the nightly lane itself is not running.

**What landed (bucket E of the census):** one shape, two sites — goopg HAD the
right check and just did not run it on the second path reaching the same state.
Hard-won Rule #2 in its least obvious form: the sibling pair is *direct target
vs recursion target* and *first column vs later column of one statement*, not
encode/decode. **E1** `cascadeNotNullToChildrenAt` bumped a child's `InhCount`
with no re-check, so a NO-INHERIT conflict on a DESCENDANT went unreported —
though `AlterTableAddNotNull` raised that exact 55000 for the ALTER target. PG
has no asymmetry because the check is INSIDE the recursion (`tablecmds.c:10012`
→ `heap.c:2609` → `AdjustNotNullInheritance`). Error names the CHILD (same rule
§36.4 found). The hunk's second `+ERROR` ("cannot drop inherited constraint")
self-resolved — the false merge made a local constraint look inherited.
**E2** duplicate explicitly-named NOT NULL in one `CREATE TABLE` → 42710 via the
existing `fkConstraintNameInUse` helper.

**Worth not re-learning:** E2's naive fix ADDED a diff line. `Catalog.CreateTable`
runs before constraint processing and is NOT transactional, so rejecting left a
phantom relation and the script's next `CREATE TABLE` failed a spurious 42P07.
PG is immune (the utility statement's txn aborts). Every post-`CreateTable`
error return in `execCreateTable` leaks this way — ledgered as a class.

**Measurement:** constraints **381 → 360 lines, 19 → 18 hunks**, no new `@@`.
Never compare to a pre-2026-08-19 number.

**Gates run:** `go build ./...` PASS; `go test ./internal/executor/
./internal/catalog/` PASS; `scripts/pg-regress-runner.sh --verbose constraints`
381/19 → 360/18; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS; both new guards FAIL-pre/PASS-post (verified by stashing); pgbench smoke
via the hook. Test cache was warm.

**Next step:** continue M0134-0005 at the **360/18** baseline. The census
(`tmp/ralph-handoffs/m0134-0005ac-census/report.md`) is still ~85% valid —
subtract buckets B, C and E; its hunk NUMBERS are stale, re-derive from a fresh
run. Top remaining, per that ranking:
1. **bucket G** — 4 independent low-risk line-pinned display/property-copy fixes
   (ATTACH PARTITION FK `_1` renaming; `\d+` sequence default not schema-
   qualified; LIKE not copying PK deferrability; the `(inherited)` tag on a
   redeclared+inherited NOT NULL). ~61 lines / 4 hunks but 4 causes — pick ONE.
2. **bucket F** — exclusion-constraint gaps, 3 hunks / 95 lines, 3 causes; the
   circle default gist opclass (catalog seed data) cascades widest.
3. **bucket D** — `pg_get_partition_constraintdef`, 1 hunk / 22 lines, breaks
   `\d+` on ANY partition; from-scratch builtin, higher risk.
Standing ledgered: chained-cast column label (bucket A), the `convalidated`
cascade (visible twice), the CREATE-time twin of E1, the phantom-relation class.

**Delegation:** `tmp/ralph-handoffs/m0134-0005ad-{research,impl}/` (researcher
`a25e5832b54c633b9` DONE 1 round; implementer `a5206c382023b7e2d` DONE 1 round,
one in-goal deviation: the compensating `DropTable`, without which the fix added
a diff line instead of removing one).

**In-flight:** none.
