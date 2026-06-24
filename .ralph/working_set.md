Loop #16: M0118-0008 `alter-table-4` **PROMOTED** (design 0118-0082) — all 4
permutations byte-for-byte vs PG 18.3 via strict `TestPort_IsolationAlterTable4`.
Committed + pushed. Spec fully closed (perms 1&2 = 0118-0080, perm 3 = 0118-0081,
perm 4 = this loop).

## What landed (perm 4 — the last permutation)
Post-lock inherited-column TYPE re-validation, mirroring PG
`make_inh_translation_list` (optimizer/util/appendinfo.c):
- `planner.SeqScan.InheritParentOID` set (beside `SkipIfVanished`) on every
  inheritance-child scan in the `allDesc` expansion loop (planner.go).
- `seqScanOp.Open`, inside the existing post-lock `skipIfVanished` block (child
  proven to still exist), calls `validateInheritedColumnTypes(im, parent, child)`
  when `inheritParentOID != 0`: match each non-dropped parent col to the child by
  name, compare `canonicalTypeClass` (collapses integer/int4, double precision/
  float8/float, resolves domains, folds IsArray, IGNORES typmod args ⇒ only a real
  base-type change trips it). Mismatch → `ExecError{42611, "attribute %q of
  relation %q does not match parent's type"}` (parent attr name + child rel name).
- Runs only for inheritance-child scans AFTER the lock (error appears post-`<...
  completed>`, not reordering `<waiting ...>`). Zero false positives.

Files: internal/planner/plan.go + planner.go; internal/executor/operators_storage.go;
internal/testport/isolation_port_test.go (TestPort_IsolationAlterTable4);
docs/design/0118-0082 + README; port-status CSV + regen md; fix_plan + ledger.

## Next step (new task — pick from M0118-0008 hard tail, all Effort-L)
alter-table-4 is DONE. Remaining M0118-0008 tail:
- **reindex-concurrently-toast**: needs real TOAST relations as catalog objects
  (reltoastrelid=0 today) + `allow_system_table_mods`; global-setup fails at
  `reltoastrelid::regclass::text`. Bigger subsystem.
- **WHERE CURRENT OF** positioned UPDATE/DELETE (project-wide; `CurrentOf` parsed,
  no executor site consumes it) — blocks detach-partition-concurrently-4's last 3
  perms.
Probe a candidate with a throwaway zz_probe test (RunAndCompare → log .Diff),
rank by first-divergence cost before committing.

## Gates run (this loop)
build+vet clean; gofmt clean (only pre-existing go1.25/1.26 noise in untouched
blocks); go test ./internal/{executor,planner,catalog}/ PASS; strict
TestPort_IsolationAlterTable4 PASS (4 perms); no regression across
AlterTable1/AlterTable3/InheritTemp strict; make ralph-state-guard OK (repaired);
pgbench smoke = pre-commit hook.
