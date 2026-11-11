# Wiki 執筆規約(書くものすべてに適用)

あなたは GitHub の Web UI でそのまま表示される Markdown の Wiki ページを生成します。
以下は例外なく守るべきルールです。

0. **出力言語は日本語です。** 本文、見出し、表の文言、`<details>` の
   summary 行、Mermaid 図のラベルはすべて日本語で書いてください。
   ただし識別子、コード、ファイルパス、`path:Symbol` 形式の引用、リンク先
   (URL や相対 `.md` パス)は原文のまま変更しません。ページ末尾の
   一覧ラベルは英語のまま `Sources:` と書いてください。ファイル名は常に英語(ASCII)です。
1. **指定されたファイルパスにのみ書き込むこと。** パスは絶対パスです。
   必要に応じて親ディレクトリを作成してください。それ以外のファイルを作成・
   変更してはいけません。
2. **すべてのページは** 1 行目を `# <タイトル>` とし、その直後に、実際に
   参照したソースファイルを列挙する `<details>` ブロックを、次の形式どおりに
   置きます。

   ````markdown
   <details>
   <summary>関連ソースファイル</summary>

   - [`internal/parser/parser.go`](https://github.com/ryogrid/goopg/blob/19856410e8d497d59787e1ab35a2febb3f5953d6/internal/parser/parser.go)

   </details>
   ````

   リンク先には、blob ベース `https://github.com/ryogrid/goopg/blob/19856410e8d497d59787e1ab35a2febb3f5953d6` が URL であればそれを使います。
   それが文字どおり "(use repo-relative links)" というテキストの場合は、
   スキームなしのリポジトリ相対パスを使ってください。実際に読んだファイル
   だけを列挙します。このブロック内で他の Wiki ページへリンクしては
   いけません — ここに載せるのはリポジトリのソースファイルだけです。
3. **主要データ構造の記述は必須です。** コンポーネントが中心的な型 —
   struct、class、enum、record、interface、型エイリアス — を軸に
   作られている場合は、それぞれの定義を **一字一句そのまま** フェンス付き
   コードブロックで引用し、`path/file.go:TypeName` の形式で出典を示して
   ください(行番号が分かれば併記)。そのうえで、重要な各フィールドが何を
   保持するか、そしてフィールド同士を結びつける不変条件を説明します。
   型の名前を挙げるだけでは不十分で、読者がその形を目で見られなければ
   なりません。ページのソースにそのような型が無い場合は、型をでっち上げず、
   そのセクション自体を省略してください。
4. **Mermaid 図は必須で、「流れ」を示すものでなければなりません。** 各ページに
   最低 1 つ(セクションページ: サブシステムの `flowchart`、リーフページ:
   実際の実行経路を示す `flowchart` または `sequenceDiagram`)。
   リクエスト/レスポンスや呼び出し順を説明する場合は `sequenceDiagram` を
   優先してください。
   - すべてのノードとエッジは、ソース中で指し示せる実在のファイル、
     パッケージ、関数、メッセージに対応していなければなりません。架空の
     コンポーネントは禁止です。
   - 1 つの図のノード数は 20 以下。大きな流れは分割してください。
   - ノード id は必ず ASCII にしてください。日本語などの非 ASCII 文字を
     含むラベルは、必ずダブルクォートで囲みます:
     `P["構文解析 Parse()"]`、`A -->|"トークン列"| B`。
   - 特殊文字を含むラベルもクォートします: `A["Parse() -> AST"]`。
   - `%%{init}%%` ブロックは絶対に出力しないでください(GitHub が除去します)。
   - `classDiagram` のジェネリクスは `~T~` で表します。例: `List~Row~`。
   - `sequenceDiagram` のメッセージ文には `;` を使わないでください — 文の
     区切りとして解釈され、パースが壊れます。読点やカンマを使うか、
     メッセージを分割してください。
   - participant/actor の名前に `loop`、`create`、`destroy`、`end`、
     `box`、`opt`、`alt`、`par`、`note` を使ってはいけません — 大文字に
     しても予約語として扱われます。短い別名を使ってください:
     `participant CP as "createplan"`。
5. **捏造の禁止。** すべての記述はソースで検証可能でなければなりません。
   書く前に必ずファイルを読んでください。挙動が不明確な場合は推測せず、
   不明であると明記してください。
6. **中身のない文章を書かない。** 「このモジュールは処理を担当します」の
   ような文は情報量がゼロです。具体的に何をするのかを述べ、主要な関数・型の
   名前を挙げ、`path/file.go:FuncName` の形式で出典を示してください。
7. **すべてのリーフページの末尾には** リポジトリ相対パスの `Sources:`
   一覧を置いてください(ラベルは英語の `Sources:` のまま)。
8. ページ間リンクは、指示された他ページのファイルへの相対 Markdown リンクに
   します。与えられていないファイルへリンクしてはいけません。
   **Wiki ページから別の Wiki ページへのリンクは、必ず、いま書いているページの
   ディレクトリを基点とした相対パスにします**(例: 兄弟ページなら
   `3.2-Wire-Protocol.md`、リーフページからトップレベルのセクションなら
   `../4-Sql-Parser.md`、トップレベルのページから子ページなら
   `3-Server/3.1-Postmaster.md`)。このリンクに URL(`https://github.com/...`、
   ホスティングされた Wiki の URL、`file:`)や絶対ファイルシステムパスを
   使ってはいけません — 本文、表、「関連ページ」リスト、Mermaid のクリック
   ターゲットのいずれでも同じです。子ページ・兄弟ページ・セクションの一覧に
   すでに相対パスで示されているものは、そのまま使ってください。
   (*リポジトリのソースファイル*(リポジトリ内の `.md` ドキュメントを含む)は
   Wiki ページではないため、規則 2 の `https://github.com/ryogrid/goopg/blob/19856410e8d497d59787e1ab35a2febb3f5953d6` の規則に従います。)
9. **コードナビゲーション用 MCP ツール**(例: `get_symbols_overview`、
   `find_symbol`、`find_referencing_symbols`、`search_for_pattern` を
   提供する Serena サーバー)が設定されている場合があります。利用可能なら、
   定義・参照・呼び出し元の特定には生の grep よりこれらを優先してください —
   シンボルを意味的に解決できます。ツール呼び出しが失敗した場合や、その
   ようなツールが無い場合はシェルコマンドにフォールバックしてください。
10. **テストコードは対象外です。** テストファイル(Go の `*_test.go`)を
   読んだり、引用したり、出典に挙げたりしないでください — `<details>`
   ブロック、`Sources:`、本文のいずれにおいてもです。たとえテストを見るのが
   使い方を知る最短の方法であっても、記述するのは本番コードだけです。


---

# Task: plan the wiki structure for `/home/ryo/work/goopg/goopg/deepwiki/.dw4m/worktree`
(タスク: `/home/ryo/work/goopg/goopg/deepwiki/.dw4m/worktree` の Wiki 構成を計画する)

あなたは `/home/ryo/work/goopg/goopg/deepwiki/.dw4m/worktree` にあるリポジトリについて、DeepWiki 風のドキュメント Wiki を
計画します。リポジトリを調査し(ディレクトリツリー、マニフェスト、README、
主要なエントリポイント)、ページツリーを作成してください。Wiki 本文は
日本語で書かれるため、ページの `title` は日本語にします。

## リポジトリの概要

## Directory statistics (files, lines of code)

- `./` — 1 files, ~292 LOC
- `internal/access/` — 165 files, ~64367 LOC
- `internal/backup/` — 12 files, ~1918 LOC
- `internal/catalog/` — 22 files, ~25681 LOC
- `internal/commands/` — 3 files, ~641 LOC
- `internal/executor/` — 247 files, ~136780 LOC
- `internal/initdb/` — 219 files, ~44734 LOC
- `internal/libpq/` — 11 files, ~4095 LOC
- `internal/nodes/` — 13 files, ~7501 LOC
- `internal/optimizer/` — 191 files, ~104429 LOC
- `internal/parser/` — 29 files, ~42993 LOC
- `internal/pl/` — 2 files, ~1868 LOC
- `internal/port/` — 10 files, ~398 LOC
- `internal/postmaster/` — 23 files, ~19896 LOC
- `internal/replication/` — 8 files, ~5003 LOC
- `internal/storage/` — 40 files, ~15214 LOC
- `internal/testport/` — 11 files, ~4821 LOC
- `internal/testutil/` — 15 files, ~5147 LOC
- `internal/utils/` — 47 files, ~12956 LOC

## Top-level overview

- `(root)` — 1 files, ~292 LOC
- `internal` — 1068 files, ~498442 LOC

## File listing (partial)

### `(root)/`
- README.md
### `internal/`
- internal/access/amcheck/bloomfilter.go
- internal/access/amcheck/heapallindexed.go
- internal/access/amcheck/heapallindexed_heapscan.go
- internal/access/amcheck/heapallindexed_relation.go
- internal/access/amcheck/verify_heapam.go
- internal/access/amcheck/verify_heapam_relation.go
- internal/access/amcheck/verify_nbtree.go
- internal/access/amcheck/verify_nbtree_rootdescend.go
- internal/access/amcheck/verify_nbtree_unique.go
- internal/access/common/blkreftable/blkreftable.go
- internal/access/common/blkreftable/serialize.go
- internal/access/common/blkreftable/testdata/array_4095.brt
- internal/access/common/blkreftable/testdata/array_4095.script
- internal/access/common/blkreftable/testdata/array_4095.txt
- internal/access/common/blkreftable/testdata/array_order_dedup.brt
- internal/access/common/blkreftable/testdata/array_order_dedup.script
- internal/access/common/blkreftable/testdata/array_order_dedup.txt
- internal/access/common/blkreftable/testdata/bitmap_at_4096.brt
- internal/access/common/blkreftable/testdata/bitmap_at_4096.script
- internal/access/common/blkreftable/testdata/bitmap_at_4096.txt
- internal/access/common/blkreftable/testdata/brt_harness.c
- internal/access/common/blkreftable/testdata/dropped_bitmap_remarked.brt
- internal/access/common/blkreftable/testdata/dropped_bitmap_remarked.script
- internal/access/common/blkreftable/testdata/dropped_bitmap_remarked.txt
- internal/access/common/blkreftable/testdata/empty.brt
- … (1043 more)

## go.mod (first 30 lines)
```
module github.com/goopg/goopg

go 1.25.0

require golang.org/x/sys v0.47.0

require github.com/lib/pq v1.12.3

require golang.org/x/text v0.21.0

require golang.org/x/tools v0.49.0
```

## Makefile (first 30 lines)
```
# Makefile — convenience targets for the goopg lifecycle.
#
# Typical flow:
#   make build      # compile ./bin/goopg
#   make init       # create a data directory under $(DATA_DIR)
#   make start      # launch the server in the background, logging to $(LOG_FILE)
#   make psql       # open psql against the running server
#   make stop       # graceful shutdown
#   make clean      # remove the data directory and the bin
#
# All targets prepend $(PG_BIN_DIR) to PATH and $(PG_LIB_DIR) to
# LD_LIBRARY_PATH so the in-tree PostgreSQL client tools (psql, pg_ctl, …)
# under postgres/local_install/ are used without polluting the operator's
# global environment.

REPO_ROOT     := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
PG_BIN_DIR    := $(REPO_ROOT)/postgres/local_install/bin
PG_LIB_DIR    := $(REPO_ROOT)/postgres/local_install/lib

DATA_DIR      ?= $(REPO_ROOT)/tmp/goopg-data
LISTEN        ?= 127.0.0.1:5432
HBA_FILE      ?=
LOG_FILE      ?= $(REPO_ROOT)/tmp/goopg.log
PID_FILE      := $(DATA_DIR)/postmaster.pid
GOOPG_BIN     := $(REPO_ROOT)/bin/goopg

# Memory-cap wrapper (OOM containment — see scripts/goopg-test-run.sh and the
# "Memory-capped execution" section of .ralph/AGENT.md). Confines a goopg run
# to a cgroup v2 scope so a runaway query is SIGKILLed inside the scope instead
# of tripping the system-wide OOM killer, which on WSL2 can take down the VM.
```

## README (first 80 lines)
[![Run Tests](https://github.com/ryogrid/goopg/actions/workflows/test.yml/badge.svg)](https://github.com/ryogrid/goopg/actions/workflows/test.yml)

# goopg

goopg is an experimental PostgreSQL-compatible database server written in Go.
It is driven entirely by coding agents as a study in agent-led implementation:
can an AI agent build and evolve a meaningful PostgreSQL-like server while
staying behaviourally aligned with upstream PostgreSQL?

The project currently targets **x86-64 Linux only** (developed and tested under
WSL2); other platforms and architectures are out of scope for now.

The project has three research axes:

1. **Agent-driven implementation** — can coding agents produce correct,
   maintainable Go code for a complex stateful system, using PostgreSQL 18 as
   the behavioural oracle?
2. **Go concurrency characteristics** — how do throughput and latency scale as
   execution paths become more concurrent?
3. **Direct I/O trade-offs** — what happens when storage paths bypass the
   OS page cache?

This repository is research-oriented and intentionally iterative. It is not
intended for production use.

The upstream PostgreSQL repository (REL_18_3) is included as a submodule
under `postgres/` and is used as the reference for correctness.

---

## Implemented Features

> **A note on "implemented":** a feature listed as implemented exists and is
> exercised, but that does **not** necessarily mean the implementation is
> complete or exhaustive — many features are partial or carry known gaps. For a
> per-feature breakdown of what is implemented, deferred, or missing, see
> [`docs/reference/coverage_table.csv`](docs/reference/coverage_table.csv).

The highlights below are a non-exhaustive excerpt of the major subsystems:

### Wire Protocol & Connection
- PostgreSQL wire protocol v3 (simple and extended query modes)
- Authentication via `pg_hba.conf`: `trust`, `reject`, `md5`, `scram-sha-256`
- GUC / `postgresql.conf` parser with `SHOW`, `SET`, `RESET`
- `pg_stat_activity` system view

### Storage & MVCC
- 8 KB heap pages in PG18-compatible on-disk format (PageHeaderData, ItemId,
  HeapTupleHeaderData)
- MVCC with snapshot isolation (READ COMMITTED and REPEATABLE READ)
- HOT (Heap-Only Tuple) updates — avoids index updates when non-indexed columns change
- Visibility Map — tracks all-visible pages for Index-Only Scan eligibility
- Index-Only Scan (single-column B-tree)
- Opportunistic page pruning — reclaims dead tuple chains inline during HOT updates
- VACUUM with tuple freeze (prevents XID wraparound)
- Autovacuum background worker
- Free Space Map (FSM) — guides INSERT to pages with sufficient free space
- Buffer pool with clock-sweep eviction
- Async I/O engine (AIO) for prefetch and parallel page reads

### Indexes
- B-tree index on integer, numeric, text, date, timestamp, boolean columns
- Unique constraint enforcement via B-tree
- Primary key constraint
- `CREATE INDEX`, `DROP INDEX`
- Index scan, range scan, index-only scan

### Query Engine
- Full SQL parser (SELECT, INSERT, UPDATE, DELETE, COPY FROM)
- Planner: sequential scan, index scan, index-only scan, hash join,
  nested-loop join, sort, aggregate, limit, project
- Multi-way bushy hash join (spill-to-disk for joins that exceed memory)
- Correlated subquery optimization and IN-unnesting
- Join-order reordering with cost-based cardinality estimates
- MCV histograms and per-column statistics (ANALYZE)
- Window functions: ROW_NUMBER, RANK, LAG, LEAD
- CTEs (WITH clause, non-recursive)
- Subqueries (EXISTS, NOT EXISTS, scalar, lateral)
- UPSERT: `INSERT ... ON CONFLICT DO UPDATE`
- SELECT FOR UPDATE / FOR SHARE (pessimistic row locking)

## 既存の Wiki 構成

(none — fresh wiki)

上にページが列挙されている場合、これはすでにページ生成済みの既存 Wiki の
再計画です — ページ番号の振り直しや名前の変更は、そのページの全面的な
再生成を招きます。したがって:

- 既存ページがまだ適切であれば、その `id`、`title`、`file` は列挙された
  とおりに一切変えずに残してください。例外は 1 つだけです: 既存の `title`
  が日本語でない場合(Wiki の言語を切り替えている場合)は `title` を日本語に
  翻訳してください — その場合も `id` と `file` は変えません。
- 新しいページは、既存の兄弟ページの後ろに新しい id を追加する形で加えて
  ください(例: 新しいトップレベルセクションは最後のトップレベル id の後、
  新しい子ページは最後の子ページの後)— 結果として用語集より後ろに
  来るとしても構いません。場所を空けるために既存ページの番号を振り直しては
  いけません。
- ページを削除するのは、その対象がリポジトリから無くなった場合だけです。

## 出力の取り決め — このファイルにのみ書き込むこと

単一の JSON ファイルを、正確に次のパスへ書き込んでください:

`/home/ryo/work/goopg/goopg/deepwiki/.dw4m/structure.json`

スキーマ:

```json
{
  "pages": [
    {
      "id": "2.3.1",
      "title": "結合の最適化",
      "file": "2-Core-Architecture/2.3-Planner/2.3.1-Join-Optimization.md",
      "parent": "2.3",
      "kind": "leaf",
      "sources": ["internal/planner/join.go"],
      "diagrams": ["sequence", "flowchart"]
    }
  ]
}
```

JSON のキー名は上記の英語のまま使ってください。

ツリーのルール:

- `id` はドット区切りの番号(`2`、`2.3`、`2.3.1`)。深さは 3 以下。
- `title` は日本語で書きます。
- `file` は id に対応させます: トップレベルページは `N-Slug.md`、子ページは
  `N-Slug/N.M-Sub.md`、深さ 3 ではディレクトリをさらに入れ子にします。
  **ファイル名(ディレクトリ名を含む)は必ず ASCII のみ** とし、日本語を
  含めてはいけません。Slug はタイトルの意味を英語にしたものの kebab-case
  です(例: タイトル `結合の最適化` → `Join-Optimization`、
  `クエリプランナー` → `Query-Planner`)。親ディレクトリ名は、親ページの
  `file` の Slug と一致させてください。
- `parent` は親ページの id、トップレベルページの場合は null。
- `kind` は `section`(子を持つ概要ページ)、`leaf`、`index` のいずれか。
  `kind: "index"` のページをちょうど 1 つ含めてください — プロジェクトの
  概要ページ(システム図とモジュールマップ)です。その `file` を
  `README.md` にしてはいけません — トップレベルの `README.md` はトピック
  索引として自動生成されます。`1-Overview.md` などにしてください。
- `sources` には、そのページが扱うべきリポジトリ相対パスのファイルを列挙
  します(リーフは 10〜30 ファイル、セクションはそれより少なめ)。自明で
  ないソースファイルはすべて、いずれかのページでカバーされるようにして
  ください — 概要を見て、どこにも割り当てていないディレクトリが無いか確認
  してください。
- `diagrams` には、そのページに含めるべき Mermaid 図の種類を宣言します。
- コードベース全体を広く、かつ深くカバーしてください: 合計 40〜120 ページを
  目安とします。ディレクトリ単位で機械的に分けるのではなく、サブシステム
  単位(例: 「パーサー」「ストレージ」「ネットワーク」)でまとめてください —
  概要と自分自身の調査をもとに、実際のアーキテクチャを見つけ出すこと。
- 幅広く浅いページよりも深いツリーを優先してください: サブシステムに
  複数の独立したコンポーネントがある場合は分割し、コンポーネントごとに
  リーフページを作ってセクションページの下にまとめます(大きな
  サブシステムでは深さ 3 を使います。例:
  `5-Query-Planner/5.2-Joins/5.2.1-Nested-Loop.md`)。1 つのリーフは 1 つの
  まとまったコンポーネントを扱うべきで、無関係なファイルの寄せ集めに
  してはいけません。
- コードベースに横断的な関心事があれば、それらのページも計画してください:
  エラー処理の方針、並行性モデル、ビルド/コード生成、設定システムなど。
  テストコード(Go の `*_test.go`)は対象外です — それについてのページは
  計画しないでください。
- 最後に、トップレベルの「用語集」リーフページを含めてください(例:
  タイトル `用語集`、ファイル名は `N-Glossary.md`)。

ファイルには JSON のみを書いてください — 最後のメッセージでの説明は不要です。


## Code-navigation tooling configured for this run

MCP server(s): serena. Symbolic navigation tools are available (e.g. Serena's `get_symbols_overview`, `find_symbol`, `find_referencing_symbols`, `search_for_pattern`). Prefer them over raw grep/read for locating definitions, references and callers — they resolve symbols semantically. If a tool call fails, fall back to shell commands.