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

# Task: write wiki page 14.2 — "PG 互換ノードシリアライザ"
(タスク: Wiki ページ 14.2 を日本語で執筆する)

Repository: `/home/ryo/work/goopg/goopg/deepwiki/.dw4m/worktree`
Write exactly this file: `/home/ryo/work/goopg/goopg/deepwiki/14-Node-IR/14.2-PG-NodeTree.md`
(上記のファイルにのみ書き込んでください。)

## このページが扱う範囲

ソースファイル:

- `internal/executor/sys_pg_attrdef.go`
- `internal/executor/viewdef_deparse.go`
- `internal/initdb/nailed_view_ev_action.go`
- `internal/initdb/pg_rewrite_bootstrap.go`
- `internal/nodes/ir.go`
- `internal/nodes/ir_query.go`
- `internal/nodes/outfuncs.go`
- `internal/nodes/outfuncs_query.go`
- `internal/nodes/readfuncs.go`
- `internal/nodes/readfuncs_query.go`
- `internal/nodes/unsupported.go`

子ページ(関係する箇所でリンクしてください — パスはすでにこのページの
ディレクトリからの相対パスになっています):

(none)

兄弟ページ(同じセクション内 — 相対リンク):

- [14.1 式 IR とリゾルバ](14.1-Expression-IR.md)

トップレベルのセクション(文脈を示すリンク用 — これも相対パスです。URL に
置き換えないでください):

- [1 プロジェクト概要](../1-Overview.md)
- [2 システムアーキテクチャ](../2-Architecture.md)
- [3 サーバーと接続](../3-Server.md)
- [4 SQL パーサー](../4-SQL-Parser.md)
- [5 クエリプランナー](../5-Query-Planner.md)
- [6 実行エンジン](../6-Executor.md)
- [7 ストレージ](../7-Storage.md)
- [8 B-tree インデックス](../8-Btree.md)
- [9 トランザクションと MVCC](../9-Transactions.md)
- [10 WAL と耐久性](../10-WAL.md)
- [11 レプリケーションとバックアップ](../11-Replication.md)
- [12 カタログとシステムビュー](../12-Catalog.md)
- [13 ユーティリティとランタイム](../13-Utilities.md)
- [14 ノード IR とシリアライズ](../14-Node-IR.md)
- [15 ビルドと開発ツール](../15-Build-and-Tooling.md)
- [16 用語集](../16-Glossary.md)

## ページ構成

見出しは以下の日本語の見出しをそのまま使ってください。

1. 1 行目は `# PG 互換ノードシリアライザ`(タイトルは日本語)、続いて `<details>`
   ソースブロック(規約を参照)。
2. 目的を述べる 1 段落 — システムのこの部分が具体的に何をするのか。
3. `## 責務` — 実際に担っている役割の箇条書き。
4. `## 処理フロー` — 名前を挙げた関数/ファイルを通る実際の実行経路を
   たどる **Mermaid の `sequenceDiagram` または `flowchart`**。
   これが最も重要なセクションです。読者が「何が存在するか」だけでなく
   「コードがどう動くか」を把握できなければなりません。
5. `## 主要ファイル` — `ファイル | 役割` の表。`path:Symbol` 形式で出典を
   示します。
6. `## 公開 API / エントリポイント` — 他のコードから呼ばれるシグネチャ
   (無ければ省略)。
7. `## 主要データ構造` — このコンポーネントの軸となる中心的な
   struct/class/record。それぞれについて、定義を一字一句そのまま
   フェンス付きコードブロックで引用し、`path:Symbol` で出典を示し、
   重要な各フィールドとそれらを結ぶ不変条件を説明します。ページのソースに
   そのような型が存在する場合は必須です — 存在しなければ省略します。
8. `## 内部構造` — 状態、特徴的なパターン、ハマりどころ(流れの理解に
   役立つなら 2 つ目の図を置いても構いません)。
9. `## エッジケースと不変条件` — 並行性のルール、エラー/失敗時の経路、
   上限値、重要な定数とその値である理由(本当に何も無い場合のみ省略)。
10. `Sources:` — 末尾に置くリポジトリ相対パスの一覧(ラベルは英語の
   `Sources:` のまま)。

密度高く具体的に書いてください: 実在する関数、struct、定数、設定項目の
名前を挙げること。読者が推測できない仕組み — 競合時の振る舞い、不正な入力に
対する振る舞い、境界での振る舞い — を説明してください。目安は 120〜400 行
ですが、コンポーネントが本当に複雑ならそれ以上でも構いません。


## Code-navigation tooling configured for this run

MCP server(s): serena. Symbolic navigation tools are available (e.g. Serena's `get_symbols_overview`, `find_symbol`, `find_referencing_symbols`, `search_for_pattern`). Prefer them over raw grep/read for locating definitions, references and callers — they resolve symbols semantically. If a tool call fails, fall back to shell commands.