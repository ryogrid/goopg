# Parser Flow

Lexer → yacc / hand-written dispatch, AST → analyzer pipeline, and PL/pgSQL
parsing.

## Lexer → Yacc / Hand-Written Dispatch

````mermaid
flowchart TD
    SQL["SQL text"] --> Lex["lexer.Lex(input)<br/>hand-written tokenizer"]
    Lex --> Toks["[]Token<br/>keyword/ident/number/string/operator"]
    Toks --> Route["dispatch.routeBatch / parseStatement"]
    Route --> YACC{"statement class?"}
    YACC -->|"bulk SQL"| Y1["yacc_parser.go (LALR(1) grammar)<br/>compiled from grammar/*.y"]
    Y1 --> Y2["yacc_ctors.go: AST constructors<br/>$<p>N position tracking"]
    YACC -->|"DDL (CREATE/ALTER/DROP/GRANT…)"| H1["hand-written ddl.go<br/>recursive descent"]
    YACC -->|"SELECT/INSERT/UPDATE/DELETE"| H2["select.go / dml.go<br/>statement tree builder"]
    YACC -->|"expressions"| H3["expr.go / function.go<br/>operators, casts, subqueries"]
    Y1 --> AST["parser.Stmt (ast.go nodes)"]
    H1 --> AST
    H2 --> AST
    H3 --> AST

    note right of Route: ~1.7% of statement classes stay on<br/>hand-written scanners (parser gap)
````

## AST → Analyzer Pipeline

````mermaid
sequenceDiagram
    participant P as parser.Parse
    participant A as analyzer.Analyze
    participant CAT as catalog.Catalog
    participant O as optimizer

    P->>P: parse SQL → []Stmt
    P-->>A: []Stmt (AST)
    loop per statement
        A->>A: resolve schema references<br/>against catalog
        A->>CAT: LookupTable / LookupColumn / LookupIndex
        CAT-->>A: table / column / index
        A->>A: type inference, coerce insertion<br/>(coerce.go, NumericCoercePrecedence)
        A-->>O: optimizer.Node IR
        O->>O: query_planner, join_search
    end
````

## PL/pgSQL Parsing

````mermaid
flowchart TD
    Body["CREATE FUNCTION … LANGUAGE plpgsql<br/>body text"] --> Parse["pl/plpgsql.Parse(src)<br/>hand-written bodyParser"]
    Parse --> Block["*Block AST (ast.go)"]
    Block --> Stmts["parseStmtList → []Stmt<br/>parseLoop / parseWhile / parseFor"]
    Stmts --> Exceptions["parseExceptionBlock<br/>EXCEPTION WHEN …"]
    Block --> Exec["executor: executePLpgSQLStmt<br/>interpreted execution"]
    Exec --> SQL["embedded SQL:<br/>recursive parser.Parse + executor"]

    note right of Exec: plpgsql is interpreted over internal/pl/plpgsql ASTs;<br/>language dispatch: dispatchStoredRoutineByLanguage
````