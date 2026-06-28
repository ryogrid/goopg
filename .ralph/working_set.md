Last landed (loop #4): M0118-0002 `predicate-gin` ENABLER (design 0118-0138,
NOT a promotion). Committed 05d747b8, pushed.

What/why: `int4[]` user array column storage round-trip. predicate-gin's global
setup (`create table gin_tbl(p int4[]); insert … select array[1] …`) failed at
INSERT with `invalid input syntax for type integer: "{1}"` — a user array column
(`catalog.Type{Name:"int4", IsArray:true}`; Name = ELEMENT type, array-ness
tracked separately) was treated as a scalar int4 at five `Type.Name`-only sites.
New `internal/executor/codec_array.go` stores array columns as PG-native
ArrayType varlena blobs (1-D no-NULL; 24B header + typalign-packed elements;
int2/int4/int8/oid/float4/float8/bool fixed + text/varchar/bpchar varlena) and
decodes to canonical `"{1,2}"` text. Wired behind `if t.IsArray` at
encodeValuePG / decodePhysicalPGValueMctx / physicalPGTypeAlign (codec.go);
insertOp integer-range coercion skips array cols (operators_storage.go);
isAssignable accepts array src→array dst (analyzer.go, fixes VALUES 42804); BOTH
simple+extended RowDescription loops advertise array OID via
catalog.ArrayOIDForBase when IsArray (server/dispatch.go, sibling paths — fixes
client-side strconv.ParseInt on "{1}"). Zero blast radius outside array columns
(all IsArray-gated; scalar paths byte-identical).

RESULT: predicate-gin first divergence advanced from permutation-0 GLOBAL SETUP
(blocked ALL perms) to first read step ra1 (`select * from gin_tbl where
p @> array[1] limit 1` → `operator @>: invalid box value`). Spec stays `failed`.

Files: internal/executor/codec_array.go (+ _test), codec.go, operators_storage.go,
internal/analyzer/analyzer.go, internal/server/dispatch.go, docs/design/0118-0138
+ README, fix_plan.md, deferral_ledger.md.

NEXT STEP (predicate-gin, in priority order):
1. `@>` array-containment operator runtime. Currently `p @> array[N]` dispatches
   to the geometric `box @> box` op → `operator @>: invalid box value`. Need
   anyarray@>anyarray element-membership semantics, routed by operand type in the
   expr/analyzer operator dispatch. Probe: `select * from gin_tbl where p @>
   array[1]`. This is the next single-loop enabler.
2. GIN page-grain SSI — goopg has no native GIN AM (USING gin index catalog-only
   → containment scans seq-scan w/ relation-grain SIREAD → over-aborts disjoint-
   key perms). REUSE the 0118-0137 grid-cell primitive (ssiGistGridCell /
   ssiRecordGistGridRead / ssiRecordGistIndexInsert) keyed on the GIN search key
   (the array element value) instead of a spatial cell. See memory
   [[goopg_gist_grid_cell_ssi]] + [[goopg_hash_index_ssi_bucket_locking]].

REMAINING failed isolation specs (2): predicate-gin (above) + deadlock-parallel
(needs parallel-query lock groups — no parallel query subsystem; infeasible).

Probe recipe: copy the cluster+lib/pq probe pattern (database/sql, buildDSN,
newCluster, mustInitStart) into internal/testport/zz_probe_test.go; for the spec
use framework.IsolationRunner{DSN}.RunAndCompare. DELETE the probe before commit.

Gates this loop: go build ./... clean; TestArrayCodecRoundTrip +
TestArrayCodecTextElementQuoting PASS; executor+analyzer+storage+catalog suites
PASS; -race codec PASS; TestPort_RegressSuite infra-timed-out on WSL2 (>9min;
IsArray-gated → scalar path structurally unchanged); pgbench smoke 0-failed
(14.3k TPS, pre-commit hook); make ralph-state-guard OK (self-repaired).
