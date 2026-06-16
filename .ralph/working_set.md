Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 85 COMPLETE,
about to commit + push. NOTHING in flight. Next loop starts slice 86.

=== DONE (this loop) — DU-002 slice 85 (refcursor / refcursor[]) ===
Gap: refcursor(1790)/_refcursor(2201) had ZERO wiring in either display fn AND
no codec entry (unlike slice 84's jsonpath which had a dead formatTypeOID case).
A declared `refcursor` col resolved to text(25) and round-tripped as text. Both
1790/2201 already seeded in pg_type_seed_data.go (varlena, align 'i', storage 'x').
Added the missing:
  1. catalog/codec.go: OIDRefcursor(1790)/OIDArrayRefcursor(2201) consts + 4-site
     (TypeNameToOID "refcursor", OIDToTypeName→"refcursor", ArrayOIDForBase,
     BaseOIDForArray).
  2. executor/expr.go: formatTypeOID scalar 1790→"refcursor" + array 2201→
     "refcursor[]"; oidToBuiltinTypeName scalar 1790 + array 2201 (both synced).
  3. executor/pg18_user_catalog_rows.go: userTypeAttrsForOID scalar 1790 + array
     2201, both varlena {len -1, byval f, align 'i', storage 'x'}.
Files: codec.go, codec_test.go, expr.go, pg18_user_catalog_rows.go,
pg18_user_catalog_rows_test.go, pgdump_connsetup_test.go (fixture rfc/rfcs +
asserts), design doc 0110-0001 slice 85 section.
Gates: gofmt clean; build ./... ok; TestTypeNameToOIDRoundTrip PASS;
TestUserPGAttributeArrayColumn PASS; TestPort_PgDumpConnectionSetup PASS (1.93s);
pgbench CI-parity smoke via pre-commit hook (pending commit).

=== NEXT STEP — DU-002 slice 86 ===
Pattern per scalar+array type: VERIFY seeded in pg_type_seed_data.go FIRST; then
check BOTH display fns (oidToBuiltinTypeName ~L10836 AND formatTypeOID ~L192/307 in
expr.go) — sibling-paths gotcha; add 4-site codec wiring + userTypeAttrsForOID
scalar+array; add fixture col(s) to `arr` table (~L625) + asserts (~L1140) in
pgdump_connsetup_test.go; run -v.
Remaining un-wired simple scalar+array candidates:
  - aclitem(1033)/_aclitem(1034): len 16, byval f, align 'd', storage 'p'. EASY next.
  - gtsvector(3642): GiST internal — likely skip (not user-declarable cleanly).
BLOCKED/LARGER (defer w/ ledger): "char"(18)/_char(1002) needs parser fold work;
range/multirange (int4range 3904 etc, has rngsubtype = LARGER); IDENTITY+SEQUENCE
(relkind 'S'); ENUM/composite/DOMAIN user types; current_schemas() name[] (slice 33).
GOTCHA: server typeOIDFor (dispatch.go) is a SEPARATE 5th type→OID fn (RowDescription
path) NOT touched by these slices; leaving types there as-is matches scope.
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd` in a Bash compound PERSISTS; use abs paths.
NOTE: only ONE live loop (2 ralph_loop.sh = parent + portable_timeout subshell child).
