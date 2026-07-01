(idle — nothing in flight)

Loop #51 COMPLETE, committed: DROP CAST type-name-synonym key fix (closes the
loop #48 deferral). `RegisterCast`/`DropCast`/`CastByTypes`
(internal/catalog/catalog.go) now key on `castKey`/`castKeyTypeName`, which
resolve each type name through `TypeNameToOID` before keying, so
`DROP CAST (real AS text)` correctly finds a cast created via
`CREATE CAST (float4 AS text) ...`. Also fixed a second latent bug found
while landing this: `TypeNameToOID` falls back to `OIDText` for any
unrecognized name (domains/enums/composites), so naively keying on its raw
result would have collapsed distinct user-defined-type casts into one
bucket — `castKeyTypeName` detects the fallback and keys on the lowercased
name instead in that case.

New tests: internal/catalog/cast_synonym_test.go (5 tests, all passing):
TestDropCastResolvesTypeNameSynonyms, TestDropCastResolvesMultiWordSynonyms,
TestRegisterCastIdempotentAcrossSynonyms, TestCastByTypesDistinctForUnrelatedTypes,
TestCastByTypesDistinctForUnknownUserDefinedTypes.

Gates run this loop: go build/vet clean; targeted cast-synonym tests PASS;
-race -count=1 on internal/wal+internal/catalog+internal/initdb PASS (263s);
internal/catalog+internal/executor full suite PASS; make ralph-state-guard
self-repaired stale progress.json marker (same pattern as prior loops) then
OK. pgbench smoke runs via the pre-commit hook on this commit.

Design doc updated: docs/design/0110-0001-pg-dump-tap-port.md, new
"DROP CAST type-name-synonym key fix" subsection. Ledger: new row appended
noting the deferred follow-up (COMMENT ON CAST synonym resolution not
independently re-verified, though it goes through the same fixed
CastByTypes choke point).

Next loop candidate: re-scan .ralph/deferral_ledger.md for the next
highest-value open "| - |" row (90+ open rows). Candidates surfaced by
this loop:
- COMMENT ON CAST synonym re-verification (internal/executor/operators_ddl.go,
  slice 396) — cheap, add one fixture confirming CastByTypes' fix covers
  the comment-resolution path too.
- ALTER COLLATION OWNER/RENAME/REFRESH VERSION (loop #50 deferral) — same
  shape as other unhandled ALTER-object gaps.
- Any other open M0119-0004 row (GRANT/ACL virtual-vs-heap blocker,
  extended-protocol commit-time deferral, etc. — see MEMORY.md's m0119
  entries for what's architecturally entangled vs. independently bounded).
Whichever is picked: update fix_plan.md AND the design doc in the SAME loop.
