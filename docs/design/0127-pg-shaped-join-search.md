# 0127 — PG-shaped join search：実装計画（leftdeep-joins バンドルの Ralph タスク分解）

| field | value |
| --- | --- |
| status | draft — **DESIGN ONLY**、実装未着手 |
| date | 2026-08-03 |
| milestone | `docs/milestones/0127-pg-shaped-join-search.md` |
| design of record | `docs/design/leftdeep-joins/` — **この文書は設計の権威ではない。** 各タスクの指示はバンドル章（`README.md`、`01`–`09`、`IMPLEMENTATION-TODO.md`）が唯一の源であり、本計画は「詳細は XX §N を参照」の形で参照する。バンドル配下のファイルは参照のみ、変更しない |
| convention | タスクは Ralph の 1 ループ（1 セッション）で完了可能なサイズ（`.ralph/PROMPT.md`「ONE task per loop」）。各タスクにゲート（完了条件）を明記。deferral = ledger 行 + unchecked box、黙って閉じない |
| 分解源 | `IMPLEMENTATION-TODO.md` の P0–P6 構造をより細粒度に分割（P5 は 9 タスク + P5.3a bushy 位相、PS6 は 2 タスクに分割して計 34 タスク） |

## 1. 位置づけ

M0126 は 2026-08-03 に documented no-go として終了した。本マイルストーンは
`docs/design/leftdeep-joins/` バンドル（ユーザー指示 2026-08-02、amended
2026-08-03）を shipped behaviour に変換する。バンドルの stop conditions は
拘束力を持つ（M0126 と同じ）。ステージ構成（S0–S7）、フラグ、rollback 契約は
[08-migration-and-removal.md](leftdeep-joins/08-migration-and-removal.md) §2
が規範。受け入れバーは [09-verification-and-acceptance.md](leftdeep-joins/09-verification-and-acceptance.md)
§3 が規範。本計画はそれらへの索引である。

**順序の原則（08 §1）：** executor first、planner second、deletion last。
P0–P4（エグゼキュータ段）は現行デフォルトプランナの出力を即座に改善し、
プランナリスクを運ばない。P5（DP）が着地する頃には、それが発行する
バイナリカスケードはすべて修繕済みエグゼキュータ上を走る。

## 2. 各タスクの共通ゲート語彙（09 §1、拘束力）

- **UNITS**：`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` green。
- **SMOKE**：全コミットの pre-commit pgbench smoke（hook、決して `--no-verify`）。
- **SPOT**：`scripts/tpch-spotcheck.sh`（Q12/Q13 canonical 行数、fresh capped server）
  — 全 planner/executor/codec コミット。
- **DS05**：`scripts/tpcds-sf05-regression.sh sweep` — row-count デルタも
  checksum デルタも**ゼロ**（git-tracked oracle に対して）。fusion を捕まえた
  ゲートであり、E1（seam）と S3（spill）の主たる正しさの計器。段ごとに、
  末尾だけではなく実行する。
- **PLAN**：plan-diff（`make plan-diff LABEL=…`、ラベルは現在の再ベースライン
  を確認 — 2026-08-03 現在 `post-mhj-retire` 系統が最新）。
- **REGRESS**：全 regress-port スイート（E1、E4、S3、S4 後 — M0106
  six-silent-regressions 前例）。
- **RACE**：`make race-gate`（共有状態に触れる段 — E3 の build 変更は
  `parallel_hash_build.go` と相互作用、S3 の temp-file registry は
  cancellation 下で Close が走る）。
- **SIBLING**：sibling-path 監査をコードレビューで明示列挙 — E4
  （planner keys ↔ executor key encode）、06 §2.1（planner nbatch ↔ executor
  nbatch）、E5（compiled ↔ interpreted evaluators）。
- **BENCH**：seam マイクロベンチ（3 段カスケード、1M 合成 probe 行 —
  定常 alloc 0）等、09 §7 の fixtures。CI ゲートではなく tripwire。

タイムド測定は **quiet host** でのみ（`pgrep -af run-nightly.sh` を先に確認）、
サーバー年齢一定（sweep-tail 規律）。`-count=1` はゲートの `go test` に渡さない。

## 3. タスク分解

### P0 — Executor pure wins [S0]（3 タスク、各 1 ループ）

無条件（フラグなし）、pure wins。S0 の出口 = units + spotcheck + pgbench smoke +
stage0 流 A/B でどのクエリも悪化なし（08 §2）。

| ID | 内容 | 参照 | ファイル | ゲート |
|---|---|---|---|---|
| **P0.1** | `mergedKeySlot` 構築を `Open` へ hoist（結合ごとに形状不変）；`.row` は pull ごとに rebind。seam マイクロベンチの定常 alloc をゼロに | IMPLEMENTATION-TODO P0.1；05 §3（E2） | `internal/executor/operators_join_agg.go`（:986-1014、build 側 :590/:646/:702、probe 側 :1266/:1269） | UNITS + SPOT + BENCH |
| **P0.2** | Single-pass build：`drainRowsBounded` の予算を `buildLazyHashTable` の build ループに折り込み、再イテレーションを削除（`rowsOp` 毎の `MaterializedSlot` alloc）。owned-copy 規律（M0097-0058）を維持 | IMPLEMENTATION-TODO P0.2；05 §4（E3） | `internal/executor/operators_join_agg.go` | UNITS + SPOT + RACE（shared-build 相互作用） |
| **P0.3** | Single-map build：planner が `planner.Join` にキー型情報を thread；executor は build 前に int64 map か string map かを選択。int64 経路を Semi/Anti に拡張（CTID 例外は維持）。`lazyHashFinalize` の dual-map ダンスを削除 | IMPLEMENTATION-TODO P0.3；05 §4（E3） | `internal/planner/`（キー型情報）+ `internal/executor/operators_join_agg.go` | UNITS + DS05 |

### P1 — The seam [S1]（3 タスク、各 1 ループ）

S1 は `GOOPG_JOIN_SLOT_CHAIN`（既定 ON、env キルスイッチ OFF のみ）。
出口 = 全 regress-port + TPC-H SF1 sweep + DS05；Q3/Q10/Q18/Q7 ≤ 1.2× R0
（8.46 / 6.04 / 27.58 / 25.13 s）。

| ID | 内容 | 参照 | ファイル | ゲート |
|---|---|---|---|---|
| **P1.1** | Legacy-path slot chaining（M0126-0004 の deferral を un-defer）：probe child slot を `lazyVirtualOut` のソースに。ポインタ変更で rebind + 型変更で copy fallback。`slotRow(probeSlot)`（:1254）と vestigial `lazyKeyRow` を削除。env キルスイッチ `GOOPG_JOIN_SLOT_CHAIN=off` | IMPLEMENTATION-TODO P1.1；05 §2（E1、F7 契約：child は安定 slot を返さない） | `internal/executor/operators_join_agg.go` | REGRESS 全 + DS05 + SPOT + BENCH（seam 0 alloc） |
| **P1.2** | Worker-path 演習：P1.1 の seam を `BuildWorker`（`inWorker=true`）下の統合テストで確認 — fusion の decline-in-worker 前例がこの経路の黙った分岐を警告している | IMPLEMENTATION-TODO P1.2 | `internal/executor/`（worker テスト） | RACE |
| **P1.3** | S1 A/B 証跡実行：Q3/Q10/Q18/Q7 ≤ 1.2× R0、他クエリは pre-S1 HEAD 比 ≤ 1.2×。証跡 `analysis/leftdeep-joins/<date>-s1-ab.txt`。バー達成または attribution（09 §6）までは P2 を開始しない | IMPLEMENTATION-TODO P1.3；09 §2/§6 | 証跡ファイルのみ（コードなし） | タイムド TPC-H SF1 A/B + SPOT per arm |

### P2 — Multi-column keys [S2]（3 タスク、各 1–2 ループ；P2.1/P2.2 は sibling 対）

S2 は plan 影響あり → plan-snapshot 再ベースラインを**同コミット**で。
`reselectDegenerateHashKeys` は P2.2 で削除（M0125-0035b が導入した
単一 equi-pair 縮退の回避策は、真の多列キーに置き換わる）。

| ID | 内容 | 参照 | ファイル | ゲート |
|---|---|---|---|---|
| **P2.1** | `planner.Join.HashKeys []JoinKeyPair`：探索/押下が全 equality conjunct を充填；residual は非等結合のみ。EXPLAIN のキーリスト描画。plan-snapshot 再ベースライン同コミット | IMPLEMENTATION-TODO P2.1；05 §5（E4 planner 側）；02 §2（BuildLeft = commutation） | `internal/planner/`（Join 構造体、探索/押下、EXPLAIN） | UNITS + SPOT + DS05 + PLAN（snapshot diff レビュー） |
| **P2.2** | Executor 複合キー：全 int64 固定幅パック；mixed は concatenated `datumKey`。`reselectDegenerateHashKeys` + その planner pass を同コミット削除。Q78 クラス退化回帰テスト追加（最初のキー列が定数ピンされても 1 バケットに劣化しないこと） | IMPLEMENTATION-TODO P2.2；05 §5（E4 executor 側）；記憶：`goopg_hash_join_single_key_degeneracy` | `internal/executor/operators_join_agg.go` + `internal/planner/`（削除） | UNITS + SPOT + DS05 + SIBLING（planner keys ↔ executor encode） |
| **P2.3** | Merge-join 多列キーを同じリストから（full-key comparator；residual は非等結合のみ） | IMPLEMENTATION-TODO P2.3；07 §2 | `internal/executor/`（merge join）+ `internal/planner/` | UNITS + SPOT + DS05 + PLAN |

### P3 — Hybrid hash spill [S3]（5 タスク、各 1–2 ループ）

S3 は `work_mem` 尊重 ON、`GOOPG_HASH_SPILL=off` 逃げ。出口 = Q21 SF1 完走
（cgroup cap 下）+ forced-spill バイト同一（09 §2）。

| ID | 内容 | 参照 | ファイル | ゲート |
|---|---|---|---|---|
| **P3.1** | `chooseHashTableSize`（planner と executor の両方から import 可能な共有 pkg）；goopg-width 認識（`48·c` + map オーバーヘッド） | IMPLEMENTATION-TODO P3.1；06 §2.1；04 §4 | 新 shared pkg（planner/executor から参照） | UNITS + SPOT |
| **P3.2** | Batch build/probe：hashvalue 接頭辞付き `spillWriter` フレーム、batch ごとの inner/outer ファイル、`HJ_NEED_NEW_BATCH` 状態を `nextLazy` に、nbatch 成長 + 打ち切り give-up + WARNING | IMPLEMENTATION-TODO P3.2；06 §2.2-2.4 | `internal/executor/operators_join_agg.go` + spill 基盤 | UNITS + DS05 + RACE |
| **P3.3** | クエリ毎 temp-file registry を `Context` に；`<datadir>/base/pgsql_tmp/` へ移設；起動時 sweep；`spillOp.Close` の unlink 漏れ修正。injected-crash テストで strays が残らないこと | IMPLEMENTATION-TODO P3.3；06 §3 | `internal/executor/`（spill registry）+ `internal/server/` | UNITS + クラッシュ注入テスト |
| **P3.4** | Semi/Anti/LEFT の per-batch 意味論（batch グローバル `antiBuildHasNull`）；shared build は nbatch > 1 で decline | IMPLEMENTATION-TODO P3.4；06 §2.5 | `internal/executor/operators_join_agg.go` | UNITS + DS05 + RACE |
| **P3.5** | EXPLAIN の `Batches:`/memory 行；forced-spill identity テスト（低 `work_mem` Q3 がデフォルトとバイト同一）。証跡 `analysis/leftdeep-joins/…-s3-spill.txt` | IMPLEMENTATION-TODO P3.5；06 §4；09 §2 | EXPLAIN + テスト + 証跡 | Q21 SF1 完走（capped）+ DS05 ゼロデルタ + RACE |

### P4 — Other join operators [S4]（4 タスク、各 1–2 ループ）

S4 は演算子ごと、plan 影響部は S5 のフラグに従う。出口 = regress-port
outer-join ファイル green + DS05。

| ID | 内容 | 参照 | ファイル | ゲート |
|---|---|---|---|---|
| **P4.1** | Streaming merge join（duplicate-group バッファ + オーバーフローファイル）；全ドレインの `runMergeJoin`/`buildMergeSide` 蓄積を削除 | IMPLEMENTATION-TODO P4.1；07 §2 | `internal/executor/`（merge join） | UNITS + REGRESS + DS05 |
| **P4.2** | Hash outer-fill：batch ごとの matched bitmap；RIGHT スイープ；FULL = LEFT fill + スイープ；planner legality 行列更新（RIGHT/FULL hash paths）。regress-port outer-join ファイル green | IMPLEMENTATION-TODO P4.2；07 §3（PG `HJ_FILL_INNER`） | `internal/executor/operators_join_agg.go` + `internal/planner/` | REGRESS outer-join ファイル + DS05 |
| **P4.3** | `Materialize` 演算子（plan node + path + rescan replay、memory→spill）；NL join は outer を stream、inner は Materialize 下に。drain-both `runNestedLoop` バッファリングと `concatRows`-per-pair を削除 | IMPLEMENTATION-TODO P4.3；07 §4 | `internal/executor/`（materialize、nested loop）+ `internal/planner/` | UNITS + SPOT + DS05 |
| **P4.4** | Lateral：outer は stream（per-outer 再実行は維持）、出力が `o.rows` に蓄積されない | IMPLEMENTATION-TODO P4.4；07 §4 | `internal/executor/`（nested loop） | UNITS + DS05 |

### P5 — The DP [S5]（9 タスク + P5.3a、各 1–2 ループ）

各タスクは `GOOPG_PGSHAPED_DP` の背後に dark 着地（soak 中 OFF）。
collapse-limit 配線は独自サブフラグ `GOOPG_PGSHAPED_COLLAPSE`（P5.8）。
soak 中の共存規則は 08 §3：searched 根はタグ付け、legacy passes はスキップ、
`reconcileNLILayout` は searched 木で no-op を assert。

| ID | 内容 | 参照 | ファイル | ゲート |
|---|---|---|---|---|
| **P5.1** | `joinrels` level リスト + relset map（`RelOptInfo` 上）；`buildInitialRels` に `PathPrebuilt` leaves（subquery/CTE/VALUES/pinned unnest — leaf-whitelist ギャップを閉じる。M0125-0037(ii) の後継でもある） | IMPLEMENTATION-TODO P5.1；03 §1-§2 | `internal/planner/`（新 DP 基盤） | UNITS + PLAN（既定 arm ZERO 差分） |
| **P5.2** | restrictInfo リスト + `hasRelevantJoinClause`；等価クラス選択度規則（推論辺：許容、二重計数なし） | IMPLEMENTATION-TODO P5.2；03 §3；04 §5 | `internal/planner/` | UNITS + PLAN（ZERO 差分） |
| **P5.3** | `joinSearchOneLevel` の位相 1+3（initial rels への clause joins；非連結 cartesian；last-ditch）；`makeJoinRel` に PG の outer/inner 印刷規約 | IMPLEMENTATION-TODO P5.3；03 §4.1-§4.2（`joinrels.c:118`、`:200-256`） | `internal/planner/` | UNITS + SPOT + PLAN |
| **P5.3a** | 位相 2 — bushy joins、PG-verbatim（03 §4.3、`joinrels.c:141-198`）：k ループは中間点まで、clauseless rel skip（:170-172）、mirror-half `first_rel` 規則（:174-177）、`have_relevant_joinclause` pair gate（:190-191）。ペア数検証（03 §7 の算術、connectivity フィルタ後） | IMPLEMENTATION-TODO P5.3a；03 §4.3 | `internal/planner/` | UNITS + ペア数検証テスト |
| **P5.4** | `addPathsToJoinrel`：hash（両 build side）、NLI+Memoize parameterised paths、merge（pathkeys 経由）、NL fallback（jointype-legal のみ、03 §5.3；FULL-without-usable-clause の error 契約）、qual 配置は最小被覆レベル、決定的タイブレーク。Parameterisation 規律（03 §9：param-aware `setCheapest`、`PATH_PARAM_BY_REL` 拒否、`ppiRows`）。NLI 束縛契約（03 §5.2：共有 eligibility fn；DP 選択 path の constructor 失敗 = 明示的な planner error） | IMPLEMENTATION-TODO P5.4；03 §5 | `internal/planner/`（path generation） | UNITS + SPOT + DS05 |
| **P5.5** | 全 live PathKinds の `createPlan` アーム → 既存 Nodes；**探索境界座標マップ**（03 §10：relid-order canonical layout — 最終 relset から合成する 1 つの map、または relid 並べ替え root Project；schema 内 ColumnRef の plan-time 断言）；pinned-spine 再解決が map を消費；searched-subtree タグ付けで legacy passes がスキップ；`reconcileNLILayout` no-op 断言 | IMPLEMENTATION-TODO P5.5；03 §10；02 §3 | `internal/planner/`（create_plan 相当）+ `internal/executor/` | UNITS + SPOT + DS05 + PLAN（snapshot 再ベースライン同コミット） |
| **P5.6** | `calcJoinrelSize` + FK-superkey 一般化 + eqjoinsel + FK clamp（04 §3.1-3.3）；quadratic build penalty 削除；estimate audit tooling（09 §5 — Q9 の連鎖は最終 joinrel で ≤ 10²× を示す） | IMPLEMENTATION-TODO P5.6；04 §3；09 §5 | `internal/planner/cardinality.go` 系統 | UNITS + DS05 + estimate audit 実行 |
| **P5.7** | nbatch 認識 `hashJoinCost`（共有 sizing fn）；LIMIT-over-join の Startup/Total 分割 | IMPLEMENTATION-TODO P5.7；04 §4；06 §5 | `internal/planner/cost_funcs.go` | UNITS + PLAN（既定 arm ZERO 差分） |
| **P5.8** | Collapse limits を PG の実際の意味論で配線（03 §6：flat comma リストは常に 1 問題；limits は sub-joinlist と explicit JOIN のみを制御；=1 pin 意味論）；explicit INNER JOIN flattening は独自サブフラグ `GOOPG_PGSHAPED_COLLAPSE` の背後（enumerator と別 soak、08 §2）；outer joins は `join_is_legal` 推論が着地するまで pinned（03 §4.4）。12 テーブル bail-out 削除 | IMPLEMENTATION-TODO P5.8；03 §6 | `internal/planner/`（collapse）+ `joinorder.go`（sequencer 降格の準備） | UNITS + DS05（サブフラグ OFF/ON 両 arm） |
| **P5.9** | S5 受け入れ実行：09 §3 の全バー（collapse OFF → ON の順に 2 回）+ plan-shape ratchet ベースライン（§4）+ estimate audit（§5）；フラグ flip または文書化 no-go。証跡 `analysis/leftdeep-joins/…-s5-acceptance.txt` | IMPLEMENTATION-TODO P5.9；09 §3-§5；08 §2 | 証跡 + フラグ flip コミット | 受け入れバー全条項 |

### PS6 — Compiled key/residual evaluation [S6]（2 タスク、各 1 ループ）

挙動中立、フラグなし。sibling-path 監査（compiled ↔ interpreted）が
リリースゲート（09 §1）。

| ID | 内容 | 参照 | ファイル | ゲート |
|---|---|---|---|---|
| **PS6.1** | `HashKeys[i]` アクセサと residual conjunction を `Open` で `ExprNode` にコンパイル（`internal/executor/exprnode.go`）；未対応種は `ExprAdapter` fallback | IMPLEMENTATION-TODO PS6.1（前半）；05 §6（E5） | `internal/executor/exprnode.go` + `operators_join_agg.go` | UNITS + BENCH（alloc 後退なし） |
| **PS6.2** | compiled ↔ interpreted の sibling 監査 + parity spot-diff（式コーパス、オーバーフローコーパス含む — 0097-0037 前例） | IMPLEMENTATION-TODO PS6.1（後半）；09 §1 SIBLING | テスト + 監査証跡 | parity コーパス + BENCH |

### P6 — Deletion [S7]（4 タスク、各 1 ループ）

S5 既定 ON が clean nightly ≥ 1 サイクルを生きてから。削除インベントリは
08 §4 が規範（S7 時に grep で再取得）。`buildBindingsPosMap`/
`applyJoinTreePosMap` は 03 §10 の境界マップが production で実証されるまで
保留（08 §4、S7 の中で最も回帰しやすい変更）。

| ID | 内容 | 参照 | ファイル | ゲート |
|---|---|---|---|---|
| **P6.1** | Fusion 削除：`fused_hash_join.go`（707 行）、hook（`executor.go:160-163`）、env vars、planner 側孤児エクスポート検査（`IsCanonicalKeyEquality` の他 caller 確認） | IMPLEMENTATION-TODO P6.1；08 §4「Fusion」 | `internal/executor/fused_hash_join.go` 削除 + hook/env | grep-clean + UNITS + SPOT |
| **P6.2** | MultiHashJoin 削除（S7 時点の fresh grep inventory；2026-08-02 時点 ~34 arms/18 files）：node、packer（`rewriteMultiWayChain`/`collectMultiHashTables`）、`mhj_input_rewrite.go`、posmaps、cost/cardinality arms、executor op（`multi_hash_join.go` 696 行）、EXPLAIN arms、`generateMultiHashJoinPath`、flags（`mhjPackingEnabled`/`GOOPG_MHJ_PACKING_OFF`） | IMPLEMENTATION-TODO P6.2；08 §4「MultiHashJoin」 | 上記 15+ ファイル | nightly green 後 + grep-clean + UNITS + SPOT + DS05 |
| **P6.3** | 旧 subset-bitmask DP + 関連族削除：`enumerateBushyPlans`/`enumerateSubsets`/`enumerateSplits`/`dp map[uint16]dpEntry`、`estimateJoinCost` + integer weights、`attachUnusedCrossEdges`、`bushySeedRowCounts`、`len(tables) > 12` cap、`IsSmallDimensionSide` pinning、`chooseInnerJoinAlgo`（searched）；subset 内 layout/remap 族（`dpEntry.layout`、`remapKeyToLayout`、`mergeSubsetLayouts`）削除；`joinorder.go` は over-limit sequencer へ降格。**`buildBindingsPosMap`/`applyJoinTreePosMap` は保留** | IMPLEMENTATION-TODO P6.3；08 §4「Planner」「layout/remap 族」 | `internal/planner/bushy.go` 等 | grep-clean + UNITS + SPOT + DS05 |
| **P6.4** | Supersession スタンプ（0034-0001、0038-0001、cost-model/09 §3 の allowance、0043/0063/0125/0126 の MHJ 章）；README 索引 status flips；skip された PG 挙動ごとの ledger 行（GEQO、skew buckets、SpecialJoinInfo in-DP — `join_is_legal` 推論依存マーカー付き —、shared spilling builds、full join_order_restriction 推論） | IMPLEMENTATION-TODO P6.4；08 §5 | 文書 + `.ralph/deferral_ledger.md` | 文書レビュー |

## 4. 依存と順序の注記

- **P1.3 の A/B 証跡は P2 開始の前提**（S1 exit バー、09 §2）。
- **P2.1/P2.2 は sibling 対**（planner キー ↔ executor key encode）、一コミット。
- **P5 の各タスクは既定 arm の plan 差分ゼロが基本**（flag OFF で inert である
  ことの証明）。P5.5 は snapshot 再ベースラインを同コミットで。
- **P5.8 は P5.3 の後に**（collapse は「何が探索に入るか」を変え、enumerator
  と coupling すると S5 の回帰が attribution 不能になる — 08 §2）。
- **P6 は S5 既定 ON が clean nightly ≥ 1 サイクルを経てから**（08 §2 S7）。
- **M0125 の並行残タスク**：exprwalk commits 5–8（M0125-0002）は P5.5 の
  searched-subtree タグ付けの基盤として先行して完了させる。M0125-0047
  （決定的タイブレーク）は P5.4 の deterministic tie-break 設計と共用のため
  先に閉じる。M0125-0040（ROLLUP）はバンドル外の独立トラック。

## 5. 証跡規約

- 全証跡は `analysis/leftdeep-joins/` 配下にコミット（09 §2 の命名規約）：
  `<date>-s1-ab.txt`、`<date>-s3-spill.txt`、`<date>-s5-acceptance.txt`、
  estimate audit（09 §5）、parity gate mismatch 記録（ratchet）。
- タイムド測定は quiet host・サーバー年齢一定・対称タイムアウト。
- 各段の no-go/attribution は 09 §6 の分類学（(a) cardinality /
  (b) plan shape / (c) cost-model realism / (d) executor）に従い、
  定数変更は class 診断なしに認めない。
