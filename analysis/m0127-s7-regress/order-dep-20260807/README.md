# Evidence — the two survivors of nightly `20260806-232940` (root-0041)

Captured 2026-08-07 by running the full `TestPort_RegressSuite` in real suite
order with `GOOPG_REGRESS_DIFF_DIR` set, which the nightly itself does not do
(filed as its own M-NIGHTLY item). Trimmed to the relevant cases; the full
capture was 34 MB of `<case>_{expected,actual,raw}.txt`.

| file | what it shows |
|---|---|
| `keep/portals_p2.before.diff` | `FETCH all in foo24/foo25` (cursors over `onek2 WHERE unique1 = 50 / 60`) return `(0 rows)` where one row is expected. The index `onek2_u1_prtl` has predicate `unique1 < 20 OR unique1 > 980`, which does NOT imply `unique1 = 50` — PG would never use it. |
| `keep/select.before.diff` | Same mechanism via `onek2_stu1_prtl` (predicate `stringu1 >= 'J' AND stringu1 < 'K'`) for the qual `stringu1 < 'B'`: every `onek2` partial-index block returns `(0 rows)`. |
| `keep/truncate.flake.diff` | UNRELATED, and not caused by the fix: FK `DETAIL:`/`HINT:` lines in a varying order (a `trunc_b` line where `trunc_d`/`trunc_c` was expected). Nondeterministic — 1 of 3 identical standalone runs. Filed separately. |
| `keep/diverging-before.txt` / `keep/diverging-after.txt` | The full set of diverging cases before (89) and after (87) the fix. `portals_p2`, `select` and `hash_index` recovered; `truncate` appears only in `after` because of the flake above, not a regression. |

Both fixed cases **pass standalone** — `onek2`'s partial indexes are created by
`create_index`, so only a full-suite pass has them. That order dependence was
the symptom three earlier loops read as "does not reproduce in isolation".
