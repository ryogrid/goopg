# Milestone 0127 — PG-shaped join search（PG 完全準拠の結合探索）

**Status:** planned
**Filed:** 2026-08-03
**Reference plan:** `.ralph/fix_plan.md`（M0127 section）
**Design of record:** `docs/design/leftdeep-joins/` — 10 章構成の設計バンドル
（README / 01–09 / IMPLEMENTATION-TODO）。**実装計画（タスク分解）は
`docs/design/0127-pg-shaped-join-search.md`**。バンドル章が設計の唯一の権威であり、
実装計画はそれを再導出せず参照する。leftdeep-joins/ 配下のファイルは変更しない。
**Prerequisites:** M0125 の残タスクのうち M0127 前に必要なもの（exprwalk 安定化
= M0125-0002 commits 5–8、決定論タイブレーク = M0125-0047）の完了。（M0125-0040 ROLLUP はバンドル外の独立トラックであり、M0127 の前提ではない。）
**Branch:** `tpcds-fix2` 派生（全実装タスクは pinned clean HEAD の git worktree で実行、
明示 pathspec でステージ、rebase/ハンドオフ後に自タスクの guard テストを再実行 — 0126 の規律を継承）

## 背景

`analysis/cost-driven-second-try-200731/` の M0126 は 2026-08-03 に **documented
no-go** として終了した（`evidence/acceptance-run-2.txt`：Q9 は hang-class のまま、
-0013 の build-side memory ペナルティが Q5 を新規に 600 s+ へ後退させた）。
M0126 の終了時点で、goopg の結合プランナは次の二重の閉塞状態にある：

- **プランナ側** — subset-bitmask DP（`enumerateBushyPlans`）は、コスト付けの
  探索空間として M0126-0013 が「良い順序が存在しない」ことを実測で示した空間に
  閉じ込められている。Q9 の順序ブロッカー（FK 連鎖 ndistinct 積の爆発 →
  class-(a)、連続 6M 行ハッシュ構築の無価格 → class-(c)）はこの探索の修正では
  解けない。
- **エグゼキュータ側** — MHJ 退役（M0126-0011、`mhjPackingEnabled=false`）後の
  バイナリカスケードは probe seam ごとに再マテリアライズするため、Q3/Q10/Q18/Q7
  が seam コストを支払う。runtime fusion（M0126-0006/-0007）は正しさのため
  恒久 OFF であり、活路ではない。

このマイルストーンは、ユーザー指示（2026-08-02、2026-08-03 修正）に基づき
`docs/design/leftdeep-joins/` バンドルを **shipped behaviour に変換する**：
結合探索を PG 18.3 の `standard_join_search` / `join_search_one_level` の完全な
3 位相（clause joins + bushy + last-ditch）に置き換え、発行されるプラン木を
PG 形状のバイナリ結合（left-deep 鎖 + bushy composite-composite）に制約し、
結合エグゼキュータを PG 級の効率（streaming probe、ゼロ中間マテリアライズ、
多列キー、work_mem-bounded hybrid hash spill）に再構築して、`MultiHashJoin` と
runtime fusion を**削除可能**にする。バンドル全体は DESIGN ONLY のままこの
マイルストーンが最初の実装主体であり、バンドルの章が実装の指示書である。

## 目的（バンドル README の scope より）

1. **PG 形状のプラン木** — 発行される全プランが PG 18.3 の `join_search_one_level`
   が生成し得る形状（left-deep 鎖 *および* bushy composite-composite）のバイナリ
   結合であること（`*planner.Join` のみ；`MultiHashJoin` はプランノードとして削除）。
2. **PG 形状の level-wise DP** — subset-bitmask DP を PG 形状の DP
   （`standard_join_search` / `join_search_one_level` アナログ、`RelOptInfo`
   pathlist 上、3 位相すべて）に置換。join メソッドは探索**内部**でコスト付けされた
   path として生成される（post-DP のメソッド書き換えなし）。
3. **融合不要の結合エグゼキュータ** — バイナリハッシュ結合カスケードが
   MHJ と実行上等価（Open 時に N 個のハッシュ表構築、streaming probe 1 回、
   中間マテリアライズゼロ）になり、さらに PG の hybrid hash spill により
   大ビルドが OOM ではなく劣化する。`MultiHashJoin` と `fusedHashJoinOp` を
   恒久的に不要にする。

## 受け入れ基準（`docs/design/leftdeep-joins/09-verification-and-acceptance.md` §3 が規範）

本セクションは 09 §3 の転写であり、fix_plan の M0127 section と milestones-README
行の同期元である。**規範は 09 §3**——09 §3 が変更されたらここから更新する。

> **M0127 受け入れバー（S5 出口）。** TPC-H SF1、arm ごとに fresh capped server、
> 対称 600 s タイムアウト、サーバー年齢一定（sweep-tail 規律）：
>
> 1. **22/22 complete** — hang / OOM / timeout / row-count mismatch ゼロ。
> 2. **総壁時間 ≤ 1.2×** — pinned R0（493.31 s）と同一 HEAD の同時期
>    integer-arm のうち速い方に対して。
> 3. **単一クエリ > 2× R0 なし** — Q9 は明示的に **≤ 170.9 s**
>    （2 × R0 の 85.46 s；integer デフォルト arm の 58.83 s が野心的目標）。
> 4. **TPC-DS SF0.5：ゼロデルタ** — row-count デルタも checksum デルタもゼロ
>    （git-tracked oracle に対して）。
> 5. **発行プランに `MultiHashJoin` なし、fusion は決してトリガーしない**
>    （両スイートの EXPLAIN スイープで検証）。
> 6. **Bushy 能力（PG 同一探索）** — PG 18.3 の EXPLAIN が bushy join spine
>    （composite ⋈ composite）を示す検索済みクエリごとに、goopg が同じ
>    composite⋈composite ペアを同じ relset 分割で生成できること（§4 parity gate
>    の spine diff で検証）。コスト定数・統計忠実度に基づく代替形状は
>    ratchet の下で許容されるが、**PG が生成できて goopg の探索が表現できない
>    bushy 形状はハード失敗**（02 の契約は「PG 同一形状」でありトレードではない）。
>
> 文書化された no-go（§6 の attribution 付き）も S5 の成功完遂である —
> その場合フラグは OFF のまま残り、バンドルのプランナ半分は設計に戻る。
> エグゼキュータ段 S0–S4 はそのゲート（下記）だけで独立に成立する。
>
> **S1 出口（実行中ゲート）：** Q3 / Q10 / Q18 / Q7 がそれぞれ R0 の
> 1.2× 以下（8.46 / 6.04 / 27.58 / 25.13 s；R0 = integer+MHJ pinned、
> 合計 493.31 s）、他クエリは pre-S1 HEAD 比 1.2× 以下。
> 証跡 `analysis/leftdeep-joins/<date>-s1-ab.txt`。
>
> **S3 出口（実行中ゲート）：** Q21 が SF1 で標準 cgroup cap・デフォルト
> `work_mem` の下で完走；強制 spill 実行（`work_mem` を下げて Q3 で
> nbatch ≥ 4）が no-spill 実行とバイト同一の結果を返す。
> 証跡 `analysis/leftdeep-joins/…-s3-spill.txt`。

## M0125 / M0126 との関係

- **M0126 の後継。** M0126-0013 の最終 no-go は `docs/design/leftdeep-joins/`
  を後継に指名した（「Q9 enumeration blocker を吸収する join-search restructure」）。
  M0126 の開いた尾（-0013 の「join-enumeration improvement or fusion-operator
  integration」残差、-0004 の slot-chaining deferral）はこのバンドルが解決する。
- **M0126-0004 の deferral は S1 で un-defer**（P1.1 = legacy-path seam
  de-materialisation が slot chaining を運ぶ — 05 §2）。
- **M0126-0011（MHJ 退役）の物理削除は P6.2**（08 §4 の削除インベントリ）。
  `mhjPackingEnabled` / `SetMHJPackingEnabled` / `GOOPG_MHJ_PACKING_OFF` は
  S7 で削除されるまで revivable のまま（08 §2 の rollback 契約）。
- **M0126-0006/-0007（fusion、恒久 OFF）の物理削除は P6.1**
  （`fused_hash_join.go`、hook、env vars、孤児エクスポート検査）。
- **M0126 の受け入れプロトコルを継承**（対称タイムアウト、順序 attribution
  分類学、class-(a)/(c) 分析 — 09 §6）。`GOOPG_COST_DRIVEN_JOINORDER` フラグと
  その文書は **S5 で退役**（`GOOPG_PGSHAPED_DP` に置換、08 §6）。
- **M0125 の特定タスクを無用化する**（このマイルストーンの実装がそれらの
  acceptance を直接引き受ける）。skip 注釈は fix_plan の各タスクに付与済み：

| M0125 タスク | 無用化の理由（引き受け先） |
|---|---|
| **M0125-0031**（warm-stats planning line：timeout クラス消滅 + ランタイム最適化） | timeout クラス消滅と Q18/Q7/Q3/Q10 の回復はこのバンドルの acceptance そのもの（09 §1/§2/§3）。残りの修復は S0–S5 の各ゲートが引き受ける |
| **M0125-0032**（TPC-H Q21 shape-class timeout） | Q21 の完走（OOM 停止の解消）は S3 出口ゲートそのもの（06 章 hybrid hash spill）+ 22/22 バー。M0077 後の形状問題は 01 §6(3) がこのバンドルの回収対象に数える |
| **M0125-0033**（TPC-DS Q18 warm 2.1× 後退） | Q18 ≤ 1.2× R0 は S1 出口ゲート（05 章 seam de-materialisation）+ 01 §6(1) |
| **M0125-0037 stage (ii)**（set-op ノードを DP が見通せない） | P5.1 の `PathPrebuilt` leaves（subquery/CTE/VALUES/pinned unnest）が leaf-whitelist ギャップを閉じる。acceptance 行（Q5 `5\|OK\|100`）は既に green（2026-07-31 実測） |
| **M0125-0041**（C3 第二半分：Q30/Q81 相関スカラー集約） | 残差は C1 = `Nested Loop (CROSS)` 形状であり、P5 の DP（join メソッドは探索内部で生成）が `-0034` の join-order arm の後継として修正する。Q30/Q81 は SF0.5 ゼロデルタゲートの対象 |

- **M0125 の残タスクのうち M0127 前に必要なものはそのまま残す：**
  - **M0125-0002（exprwalk commits 5–8）** — walker 基盤の安定化。P5.5 の
    searched-subtree タグと「legacy passes が searched 木をスキップ」規約、
    P6.3 の旧 DP 削除はこの基盤の上に乗る。8 コミット中の 4 コミットは完了済み。
  - **M0125-0047（restart 非決定論タイブレーク）** — P5.4 の deterministic
    tie-break と P5.9 の plan-shape ratchet が前提とする EXPLAIN A/B の
    整合性のために先行して閉じる。
  - **M0125-0040（C6 ROLLUP → 多分岐 UNION ALL）** — grouping-sets の
    `AGG_MIXED`/`AGG_SORTED` はバンドル外の Aggregate 機能であり、
    MHJ 削除後も N 回スキャン構造は残る。独立に進行する。
  - **M0125-0013 bookkeeping half**（Q47 の 8.4× runtime の文書矛盾の
    裁定）— 文書修復であり、quiet host を要する以外 M0127 と無関係。
  - **M0125-0003 stage 3**（`estimateBaseRelInfo.baseRows`）— relsize
    fallback は S-cold 安全網（warm では `RowCount > 0` 早期 return で不活性、
    2026-07-30 実測）。stage 3 を旧 DP に着地させる前に P5.1/P5.6 の
    rows-once-per-RelOptInfo 設計と突き合わせる（M0127 の 04 §2 が base rel
    rows の供給源を再定義する可能性があるため、着地は M0127 P5.1 以降に
    再評価）。
- **M0125 で閉じたが M0127 が削除するメカニズム**（削除は P6.2/P6.3 の
  inventory で実施；supersession スタンプは P6.4）：
  M0125-0035b の `reselectDegenerateHashKeys`（→ P2.2 で削除、
  Q78 クラス退化回帰テストを同コミットで追加）、M0126-0011 の
  `rewriteMultiWayChain`/`multi_hash_join.go`（→ P6.2）、M0126-0006/-0007 の
  `fusedHashJoinOp`/`tryFuseHashCascade`（→ P6.1）、M0126-0004 の
  slot-chaining deferral（→ P1.1 で un-defer）。

## ステージ構成（08 §2 が規範）

| stage | 内容 | フラグ / 既定 | 前進ゲート |
|---|---|---|---|
| **S0** | E2（`mergedKeySlot` hoist）+ E3（single-pass single-map build） | なし — 無条件（pure wins） | units + spotcheck + pgbench smoke；stage0 流 A/B でどのクエリも悪化なし |
| **S1** | E1（legacy-path seam de-materialisation） | `GOOPG_JOIN_SLOT_CHAIN` 既定 ON、env キルスイッチ OFF | 全 regress-port + TPC-H SF1 sweep + SF0.5 checksum gate；Q3/Q10/Q18/Q7 ≤ 1.2× R0 |
| **S2** | E4（多列キー、planner+executor） | plan 影響あり → 同コミットで plan-snapshot 再ベースライン | spotcheck + SF0.5 + Q78 クラス退化プローブ；`reselectDegenerateHashKeys` 同コミット削除 |
| **S3** | 06 章 hybrid hash spill | `work_mem` 尊重 ON；`GOOPG_HASH_SPILL=off` 逃げ | Q21 SF1 完走（cgroup cap 下）；no-spill プランはバイト同一結果 |
| **S4** | 07 章 §§2–4（streaming merge、hash outer-fill、Materialize+NL） | 演算子ごと、plan 影響部は S5 のフラグに従う | regress-port outer-join ファイル；SF0.5 |
| **S5** | 新 PG 形状 DP（03 章、3 位相すべて + bushy）+ コスト結合（04 章） | `GOOPG_PGSHAPED_DP` — soak 中 OFF、acceptance イベントとして ON；`GOOPG_COST_DRIVEN_JOINORDER` は退役。collapse-limit 配線は独自サブフラグ `GOOPG_PGSHAPED_COLLAPSE` で別 soak | 上記受け入れバー全条項（collapse OFF → ON の順に 2 回） |
| **S6** | E5（compiled key/residual eval） | なし（挙動中立） | units + 式コーパスの parity spot-diff |
| **S7** | 削除（08 §4） | なし — S5 既定 ON が clean nightly ≥ 1 サイクル後のみ | nightly green；grep-clean inventory |

Rollback 物語（08 §2）：S0/S2/S6 はコミット revert、S1/S3 は env スイッチ、
S5 は `GOOPG_PGSHAPED_DP` OFF で現行 `tryBushyDP` 探索（自身 bushy 可能）に
復帰 — 旧探索は S7 まで削除しない。

## Required Design Docs

| Task | 内容 | 設計の所在 |
|---|---|---|
| 実装計画全体 | P0–P6 の Ralph タスク分解（34 タスク、P2–P5 は各 1–2 ループ） | `docs/design/0127-pg-shaped-join-search.md`（このマイルストーンが作成） |
| 各タスク | タスクごとの thin implementation spec | 実施ループが repo rule に従い `docs/design/0127-<task>-<short-slug>.md` を同一ループで作成・索引。バンドル章が設計の権威であり、thin spec は「バンドル XX §N の翻訳」に留める |

## Order

```
M0125 残タスク（exprwalk 5–8 / -0047 / -0040 / -0013 等）→
P0.1 → P0.2 → P0.3 →            [S0: executor pure wins]
P1.1 → P1.2 → P1.3 →            [S1: the seam + A/B 証跡]
P2.1 → P2.2 → P2.3 →            [S2: 多列キー（planner+executor 一コミット対）]
P3.1 → P3.2 → P3.3 → P3.4 → P3.5 → [S3: hybrid hash spill]
P4.1 → P4.2 → P4.3 → P4.4 →     [S4: 他結合演算子]
P5.1 → P5.2 → P5.3 → P5.3a → P5.4 → P5.5 → P5.6 → P5.7 → P5.8 → P5.9 → [S5: DP、各 1–2 ループ]
PS6.1 → PS6.2 →                  [S6: compiled eval]
P6.1 → P6.2 → P6.3 → P6.4 →     [S7: 削除、clean nightly ≥ 1 サイクル後]
```

- P0–P4 は現行デフォルトプランナの出力を即座に改善する（executor first —
  M0125-0002 の「方向はクエリごとに予測不能、コミットごとに計測」教訓）。
- P5 の各タスクは `GOOPG_PGSHAPED_DP` の背後に dark 着地（soak 中の共存規則
  は 08 §3：searched 根はタグ付けされ、legacy passes はタグ付き部分木を
  スキップする；`reconcileNLILayout` は searched 木で no-op を assert）。
- P5.8 の collapse-limit は `GOOPG_PGSHAPED_COLLAPSE` サブフラグで別 soak。
- 削除（S7）は S5 既定 ON が clean nightly ≥ 1 サイクルを生きてから。

## Definition of Done

1. **S0–S4 が各ゲートで完走**：pure wins（S0）→ seam 証跡（S1、`s1-ab.txt`、
   Q3/Q10/Q18/Q7 ≤ 1.2× R0）→ 多列キー（S2、snapshot 再ベースライン、
   `reselectDegenerateHashKeys` 削除 + Q78 退化回帰テスト）→ spill（S3、
   Q21 SF1 完走 + forced-spill バイト同一）→ 他演算子（S4、regress-port
   outer-join ファイル green）。各段の証跡は `analysis/leftdeep-joins/` に
   コミットされる。
2. **S5 の探索が PG の全 3 位相を実装**：clause joins / bushy / last-ditch
   （03 §4、`joinrels.c:118 / :141-198 / :200-256` アナログ）。bushy 位相は
   `joinrels.c:141-198` の PG-verbatim 構造（k ループ、clauseless rel skip、
   mirror-half `first_rel` 規則、`have_relevant_joinclause` pair gate）。
3. **メソッドが探索内部で生成**：`addPathsToJoinrel`（hash 両 build side、
   NLI+Memoize parameterised、merge、NL fallback）。post-DP の
   `rewriteJoinsToNLI`/qual-placement passes は searched 木でスキップ。
4. **`GOOPG_PGSHAPED_DP` の flip または文書化 no-go**：受け入れバー全条項を
   計測（collapse OFF → ON の順）、met → 既定 ON + snapshot 再取得 +
   「ships off by default」文の全更新；missed → no-go 文書（failing clause、
   残クエリ、attribution、後継指名）。**計測なしの outcome が唯一の失敗。**
5. **`MultiHashJoin` と fusion が削除される**：S7 で 08 §4 の inventory に
   沿って（MHJ ~34 arms/18 files、fusion 707 行 + hook + env vars）、
   旧 subset-bitmask DP + layout/remap 族 + integer cost + `IsSmallDimensionSide`
   pinning + `chooseInnerJoinAlgo` も削除。`buildBindingsPosMap`/
   `applyJoinTreePosMap` は 03 §10 の境界座標マップが production で実証される
   まで保留（S7 の中で最も回帰しやすい変更として明示的に hold back）。
6. **supersession スタンプ**：0034-0001 / 0038-0001 / cost-model/09 §3 /
   0043 / 0063 / 0125 / 0126 の MHJ 章に `superseded by: leftdeep-joins/` ヘッダ（削除しない）、
   README 索引の status flip、skip された PG 挙動（GEQO、skew buckets、
   `join_is_legal` 推論依存の semi/anti-in-DP と join_order_restriction 等）に
   `.ralph/deferral_ledger.md` 行。
7. **PG plan-shape parity gate**（`scripts/pg-plan-shape-diff.sh --strict`）が
   報告モード + pinned mismatch budget（ratchet）で S5 以降の全計画コミットを
   通る — mismatch 数はコミット間で増えない。**`expected-bushy` カテゴリは
   存在しない**（goopg は bushy 位相を実装済みなので、PG が選び goopg が
   生成できない bushy spine は真の divergence）。
8. マイルストーン索引行更新、このファイルの status は `accepted`。

## 証跡レジャー

| artefact | owed by |
|---|---|
| `analysis/leftdeep-joins/<date>-s1-ab.txt` | M0127-P1.3（S1 exit） |
| `analysis/leftdeep-joins/…-s3-spill.txt` | M0127-P3.5（S3 exit） |
| `analysis/leftdeep-joins/…-s5-acceptance.txt` | M0127-P5.9（S5 exit） |
| `plan_snapshots/` 再ベースライン（S2、S5、flip） | M0127-P2.1、P5.5、P5.9 |
| estimate audit（Q9 最終 joinrel ≤ 10²×） | M0127-P5.6（09 §5） |
| 削除前 grep inventory（S7 時点で再取得） | M0127-P6.2 |
| parity gate mismatch 記録（ratchet） | M0127-P5.9 以降の各計画コミット |

## Out of scope

- **GEQO**（遺伝的探索の移植）— 03 §7。
- **Parallel hash build**（leader-serial shared build は現状維持 —
  `docs/design/parallel-query/` が所有）。
- **`join_is_legal` 制約推論** — v1 では outer/semi/anti join は opaque pinned
  入力のまま（03 §4.4 の一時措置）。ただし bushy 位相自体（03 §4.3）は
  **v1 に含まれる**。
- **Extended statistics、bitmap heap scans**。
- **新しい executor IR**（`create_plan` は既存の `Operator` ノードに翻訳）。
- **Grouping-sets `AGG_MIXED`/`AGG_SORTED`**（M0125-0040 が独立に進行）。
- 任意の `./postgres/` 変更。

## PostgreSQL References

- `postgres/src/backend/optimizer/path/joinrels.c` — `join_search_one_level`
  （:73）、`make_rels_by_clause_joins`（:118、clauseless cartesian :120-137）、
  bushy phase（:141-198）、last-ditch（:200-256）
- `postgres/src/backend/optimizer/path/joinpath.c` — `add_paths_to_joinrel`（:124）
- `postgres/src/backend/optimizer/util/pathnode.c` — `add_path`（dominated path 即死）
- `postgres/src/backend/optimizer/path/costsize.c` — `initial/final_cost_hashjoin`
  （:4134/:4160）
- `postgres/src/backend/executor/nodeHash.c` — `ExecChooseHashTableSize`（:658）、
  `get_hash_memory_limit`（:3622）
- `postgres/src/backend/executor/nodeHashjoin.c` — build-side-only
  materialisation、pipelined outer
- `postgres/src/backend/optimizer/plan/initsplan.c` — `distribute_restrictinfo_to_rels`
- `postgres/src/backend/optimizer/plan/planner.c` — collapse limits、
  `preprocess_grouping_sets`（参照のみ；grouping-sets 自体は範囲外）
