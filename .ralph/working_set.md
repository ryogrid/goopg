(idle — nothing in flight)

Loop #40 landed and committed clean: M0119-0004 DU-002 slice 414 (`FOR ORDER
BY` sort-family resolution for `CREATE OPERATOR CLASS`). Closes the loop
#37/#39 ledger rows' "FOR ORDER BY sort-family resolution" deferral.

What landed:
- `parseCreateOpClassTail`'s `opclass_purpose` branch (internal/parser/ddl.go)
  now captures `FOR ORDER BY family_name` onto `OpClassMember.SortFamilySchema`/
  `SortFamilyName` (internal/parser/ast.go) instead of discarding it.
- `catalog.AmOpMember` gains `SortFamilyOID`; `pg_amop.VirtualRows` derives
  `amoppurpose` ('o'/'s') from it and renders the real `amopsortfamily`;
  `dependVirtualRows` emits the extra NORMAL pg_depend row on the sort family
  (mirrors storeOperators, opclasscmds.c).
- `registerOpClassMembers` (internal/executor/operators_ddl.go) resolves the
  sort family against the BTREE access method unconditionally (confirmed:
  `get_opfamily_oid(BTREE_AM_OID, ...)` is NOT parameterized by the class's
  own method) — 42704 if missing.
- **Key discovery via live PG 18.3 diff**: FOR ORDER BY is legal on
  essentially no AM except gist/spgist (`amcanorderbyop`, only gist.c/
  spgutils.c set it true upstream). Added an AM-restriction check (42P17,
  PG's exact error text) since goopg has no amcanorderbyop concept at all.
- Verified byte-identical vs a live, freshly-built PG 18.3 instance: both the
  accept path (USING gist class, raw pg_amop row columns) and the reject
  path (USING btree + FOR ORDER BY -> identical error text).
- Tests: TestParseCreateOperatorClassForOrderBy (parser);
  TestCreateOperatorClassForOrderBySortFamily/
  TestCreateOperatorClassForOrderByUnknownFamilyErrors/
  TestCreateOperatorClassForOrderByRejectsNonOrderingAM (executor).
- Gates all green: build/vet clean; catalog+executor+parser+planner+server
  suites PASS; TestPort_PgDumpConnectionSetup PASS; TPC-H spotcheck
  Q12=2/Q13=33 PASS; gofmt drift confirmed pre-existing (git stash check);
  pgbench smoke = pre-commit hook (runs on `git commit`).
- Design doc updated (docs/design/0119-0004-create-operator-roundtrip.md,
  "Loop #40" section) + docs/design/README.md index row appended + deferral
  ledger row appended (.ralph/deferral_ledger.md, slice 414).

New discovery deferred (ledger row appended, NOT attempted this loop): real
PG's `gistadjustmembers` (the only `amadjustmembers` override in the whole
in-tree AM set) forces EVERY GiST opclass OPERATOR member — not just FOR
ORDER BY ones — to a *soft* family-level pg_depend dependency
(refclassid=pg_opfamily, deptype 'a'), never goopg's unconditional *hard*
opclass-level pair ('n'+'i'). This means `dumpOpclass`'s own AS-list query
can never surface a GiST/SP-GiST opclass member in real PG's own dump at
all — it needs the separate, not-yet-implemented `ALTER OPERATOR FAMILY ...
ADD` loose-member statement (loop #34's still-open resume point (a)). This
pre-dates this loop (applies to any GiST/SP-GiST member) but this loop's
diffing is what surfaced it, since FOR ORDER BY forced a GiST fixture into
existence for the first time.

Next candidates (backlog, per the deferral ledger's open rows):
(1) Per-AM `amadjustmembers` dependency-strength policy (gist/spgist soft
deps) + `ALTER OPERATOR FAMILY ... ADD` loose-member statement — the two
naturally combine into one follow-up (needed together for any real
GiST/SP-GiST opclass to round-trip through pg_dump correctly). Materially
larger, own design-doc-level follow-up. (2) Extend the builtin-operator
catalog incrementally as new fixtures need different builtin operators, OR
generate a full leaf-package index via a new `-names` mode on
cmd/gen-pg-operator-data. (3) M0119-0005/0006/0007 (pg_waldump/pg_amcheck/
pg_basebackup server tiers). (4) M0119-0002 (CLOG store swap Part B) —
flagged highest blast radius, needs dedicated full-gate session.
(5) `op_class_custom` ordering fixture (range-type subtype_opclass binding)
— still untested, and now known to ALSO need the GiST loose-member path
above since it's a GiST-family opclass.

Recommendation for next loop: (1) is now the most valuable next step for
M0119-0004's op_class/op_family fixture family, since it's the blocker for
ANY real GiST/SP-GiST opclass (not just FOR ORDER BY) round-tripping
correctly, and directly unblocks the still-open `op_class_custom` fixture.
It's a genuinely larger slice (new per-AM policy table + a new DDL
statement + its own dump query) — consider a dedicated design-doc session
rather than a single bounded loop.
