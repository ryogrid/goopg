# M0147-0050 — recon: `goopg.enable_nestloop_index` / `rewriteJoinsToNLI` and the post-search `jn.Algo` assignment

Date: 2026-10-27, at `5a9b2fb84`.

## 1. The GUC off, three corpora

`PGOPTIONS='-c goopg.enable_nestloop_index=off'` was passed to `scripts/jointree-parity-capture.sh`. The census read
TPC-H 22/22, TPC-DS SF0.25 99/99 and SF1 99/99. The EXPLAIN text was byte-identical to the ON captures
(`analysis/m0146/m0146-0014/routing-20261027i/`), with 0 differing lines on each corpus.

## 2. Reachability probe (temporary, never committed)

A two-line probe in `walkRewriteNLI`'s `*Join` arm appended to `$GOOPG_M0050_PROBE`:
- `join-unsearched type=… algo=…` for every join the walk reaches. `isSearchedTree` stops the walk at every
  search-built subtree, so these are exactly the joins the PG-shaped search did not build, and so the only customers of
  the post-search `jn.Algo` assignment in `planFromItem`.
- `nli-built type=…` for every successful `tryBuildNLI`.

| run | unsearched joins reached | NLI rewrites | file |
|---|---|---|---|
| TPC-DS SF0.25 | 2 × `type=3 algo=2` (FULL, Merge) | 0 | `nliprobe-tpcds-sf025-summary.txt` |
| TPC-DS SF1 | 2 × `type=3 algo=2` (FULL, Merge) | 0 | `nliprobe-tpcds-sf1-summary.txt` |
| TPC-H | none recorded; the TPC-H lane's servers may not inherit the env (its flag stamps were also missing in the 0048/0049 measurements), so this is unconfirmed | — | — |
| regress suite | 1306 | 20 (12 LEFT, 8 INNER) | `nliprobe-regress-summary.txt` |

Enum key: JoinType 0 Inner, 1 Left, 2 Right, 3 Full, 4 Cross, 5 Semi, 6 Anti. JoinAlgo 0 NestedLoop, 1 Hash, 2 Merge.

The TPC-DS FULL joins match PG at both scales; the plans are MATCH in the census.

## 3. Regress with the pass disabled

A second temporary toggle made `rewriteJoinsToNLI` return early. With it, `TestPort_RegressSuite` still PASSes. The
same 81 cases mismatch in both arms; `GOOPG_REGRESS_DIFF_DIR` compared the two arms. Ten mismatching cases' outputs
differ between the arms:
- six (`copy`, `insert`, `misc_functions`, `generated_stored`, `generated_virtual`, …) differ only in temp file names
  inside psql error lines;
- `random`, `uuid`, `copy2` and `plpgsql` are nondeterministic or known flappers;
- `foreign_key` differs in the row order of an unordered catalog listing, which is plan-dependent or nondeterministic.

No case flips between match and mismatch.

## Conclusions

- `rewriteJoinsToNLI` never fires on the corpora and is invisible to every regress verdict. It fires 20 times on
  regress-only unsearched shapes. PG has no such pass: its parameterised index nested loop is an ordinary path from
  `add_paths_to_joinrel` / `match_unsorted_outer` (`postgres/src/backend/optimizer/path/joinpath.c`).
- The post-search `jn.Algo` assignment serves only trees the search did not build: 2 FULL joins per TPC-DS corpus,
  which match PG, and 1306 regress joins, of which 889 are CROSS joins.
