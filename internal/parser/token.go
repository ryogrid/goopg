// Package parser implements goopg's SQL lexer, parser, and AST.
//
// Scope and growth path are documented in docs/design/0010-parser.md.
// v0 covers the pgbench surface area with a hand-written
// recursive-descent parser; statements are added incrementally.
package parser

// TokenKind classifies a lexer token.
type TokenKind int

const (
	TokenEOF TokenKind = iota
	TokenIdent
	TokenQuotedIdent
	TokenIntLit
	// TokenNumericLit covers decimal and scientific-notation literals
	// (`1.5`, `1e10`, `0.5e-3`). v0 stores the literal verbatim — the
	// parser emits a NumericConst that the executor encodes as a
	// varlen text payload for NUMERIC columns. Real arithmetic on
	// NUMERIC waits on the type system.
	TokenNumericLit
	TokenStringLit
	// TokenBitStringLit covers both bit-string (B'...') and hex-string
	// (X'...') literals (PG SQL99 bit-type literal syntax; scan.l's
	// xbstart/xhstart, gram.y AexprConst BCONST/XCONST). Value carries a
	// leading marker byte ('b' or 'x', mirroring scan.l's own convention
	// of prefixing addlitchar('b'|'x', ...) onto the buffer) followed by
	// the raw, unvalidated source digits — decodeBitStringLit in select.go
	// validates and decodes it. M0134-0092.
	TokenBitStringLit
	TokenParam // $N
	TokenSymbol
	TokenOperator
	TokenKeyword
)

// Keyword carves out the subset of identifiers the parser treats as
// keywords. v0 only enumerates the ones the phase-1 statements need;
// each new statement family adds its own. Mirrors upstream's
// kwlist.h subset.
type Keyword string

const (
	KwBegin       Keyword = "begin"
	KwCommit      Keyword = "commit"
	KwRollback    Keyword = "rollback"
	KwAbort       Keyword = "abort" // alias for ROLLBACK in upstream
	KwEnd         Keyword = "end"   // alias for COMMIT in upstream
	KwTransaction Keyword = "transaction"
	KwWork        Keyword = "work"
	KwVacuum      Keyword = "vacuum"
	KwAnalyze     Keyword = "analyze"
	KwAnalyse     Keyword = "analyse" // British spelling, accepted upstream
	KwVerbose     Keyword = "verbose"
	KwShow        Keyword = "show"
	KwSet         Keyword = "set"
	KwLocal       Keyword = "local"
	KwSession     Keyword = "session"
	KwTo          Keyword = "to"
	KwReset       Keyword = "reset"
	KwAll         Keyword = "all"
	KwDefault     Keyword = "default"
	KwSelect      Keyword = "select"
	KwDistinct    Keyword = "distinct"
	KwFrom        Keyword = "from"
	KwWhere       Keyword = "where"
	KwAs          Keyword = "as"
	KwOrder       Keyword = "order"
	KwBy          Keyword = "by"
	KwGroup       Keyword = "group"
	KwHaving      Keyword = "having"
	KwAsc         Keyword = "asc"
	KwDesc        Keyword = "desc"
	KwLimit       Keyword = "limit"
	KwOffset      Keyword = "offset"
	KwJoin        Keyword = "join"
	KwInner       Keyword = "inner"
	KwLeft        Keyword = "left"
	KwRight       Keyword = "right"
	KwFull        Keyword = "full"
	KwCross       Keyword = "cross"
	KwNatural     Keyword = "natural"
	KwOuter       Keyword = "outer"
	KwUnion       Keyword = "union"
	KwIntersect   Keyword = "intersect"
	KwExcept      Keyword = "except"
	KwAnd         Keyword = "and"
	KwOr          Keyword = "or"
	KwNot         Keyword = "not"
	KwNull        Keyword = "null"
	KwTrue        Keyword = "true"
	KwFalse       Keyword = "false"
	KwIs          Keyword = "is"
	KwInsert      Keyword = "insert"
	KwInto        Keyword = "into"
	KwValues      Keyword = "values"
	KwUpdate      Keyword = "update"
	KwDelete      Keyword = "delete"
	KwReturning   Keyword = "returning"
	KwCreate      Keyword = "create"
	KwTable       Keyword = "table"
	KwIndex       Keyword = "index"
	KwUnique      Keyword = "unique"
	KwOn          Keyword = "on"
	KwUsing       Keyword = "using"
	KwUnlogged    Keyword = "unlogged"
	KwDrop        Keyword = "drop"
	KwIf          Keyword = "if"
	KwExists      Keyword = "exists"
	KwTruncate    Keyword = "truncate"
	KwWith        Keyword = "with"
	KwTablespace  Keyword = "tablespace"
	KwPrimary     Keyword = "primary"
	KwKey         Keyword = "key"
	KwCascade     Keyword = "cascade"
	KwRestrict    Keyword = "restrict"
	KwAlter       Keyword = "alter"
	KwAdd         Keyword = "add"
	KwConstraint  Keyword = "constraint"
	KwColumn      Keyword = "column"
	KwForeign     Keyword = "foreign"
	KwReferences  Keyword = "references"
	KwDeferrable  Keyword = "deferrable"
	KwCopy        Keyword = "copy"
	KwCheckpoint  Keyword = "checkpoint"
	KwCase        Keyword = "case"
	KwWhen        Keyword = "when"
	KwThen        Keyword = "then"
	KwElse        Keyword = "else"
	KwExplain     Keyword = "explain"
	KwIn          Keyword = "in"
	KwView        Keyword = "view"
	KwReplace     Keyword = "replace"
	KwLike        Keyword = "like"
	KwIlike       Keyword = "ilike"
	KwEscape      Keyword = "escape"
	// KwSimilar is `a_expr SIMILAR TO a_expr [ESCAPE a_expr]` (PG oracle:
	// postgres/src/backend/parser/gram.y:15080-15115, kwlist.h SIMILAR is
	// TYPE_FUNC_NAME_KEYWORD like LIKE/ILIKE). M0134-0070.
	KwSimilar     Keyword = "similar"
	KwBetween     Keyword = "between"
	KwSymmetric   Keyword = "symmetric"
	KwAsymmetric  Keyword = "asymmetric"
	// M0008 logical-replication DDL keywords. See
	// docs/design/0008-0003-publication-subscription-ddl.md.
	// Option names (publish / enabled / slot_name) intentionally
	// stay as plain identifiers so they don't collide with column
	// references like `pg_stat_replication.slot_name`.
	KwPublication  Keyword = "publication"
	KwSubscription Keyword = "subscription"
	KwConnection   Keyword = "connection"
	KwFor          Keyword = "for"
	KwTables       Keyword = "tables"

	// CTE / WITH clause (M0016).
	KwRecursive Keyword = "recursive"

	// UPSERT (M0017): `INSERT … ON CONFLICT … DO {NOTHING |
	// UPDATE SET …}`. `excluded` is intentionally NOT a keyword
	// — upstream treats it as a pseudo-table identifier resolved
	// at analyze time, so leaving it as a normal ident keeps
	// existing column-named-`excluded` queries unbroken.
	KwConflict Keyword = "conflict"
	KwDo       Keyword = "do"
	KwNothing  Keyword = "nothing"

	// Window functions (M0020): `func() OVER ( [PARTITION BY …]
	// [ORDER BY …] )`. KwOrder / KwBy already exist; the new
	// ones are OVER / PARTITION. Frame clauses (ROWS / RANGE /
	// GROUPS) and named-window references are deferred to a
	// later slice.
	KwOver      Keyword = "over"
	KwPartition Keyword = "partition"

	// Pessimistic row locking (M0021): `SELECT ... FOR { UPDATE
	// | SHARE } [ OF table [, ...] ] [ NOWAIT | SKIP LOCKED ]`.
	// `KwFor`, `KwUpdate` already exist; the new ones are
	// SHARE / OF / NOWAIT / SKIP / LOCKED. `FOR NO KEY UPDATE`
	// and `FOR KEY SHARE` are out of scope for v0 (need NO + KEY
	// composite keywords plus an extended LockStrength enum).
	KwShare  Keyword = "share"
	KwOf     Keyword = "of"
	KwNowait Keyword = "nowait"
	KwSkip   Keyword = "skip"
	KwLocked Keyword = "locked"

	// Stored routines, Stage A — function-first delivery (M0015).
	// Stage A grammar:
	//   CREATE [OR REPLACE] FUNCTION name([arg_decl, ...])
	//     RETURNS rettype
	//     [LANGUAGE lang]*
	//     AS $$body$$
	//   DROP FUNCTION [IF EXISTS] name [(arg_decl, ...)]
	//
	// CALL / PROCEDURE / OUT / INOUT / VARIADIC arrive in
	// Stage B — those keywords stay deferred.
	KwFunction Keyword = "function"
	KwReturns  Keyword = "returns"
	KwLanguage Keyword = "language"

	// PL/pgSQL body keywords (M0015 Stage A step 4). The PL/pgSQL
	// parser lives in `internal/plpgsql/` but reuses goopg's main
	// SQL lexer for tokenisation, so the keywords belong here.
	// Step 4a covers BEGIN / END (already declared above) and
	// RETURN; DECLARE / IF / LOOP / WHILE / FOR / EXIT / CONTINUE /
	// PERFORM stay deferred to subsequent slices.
	KwReturn   Keyword = "return"
	KwDeclare  Keyword = "declare"
	KwConstant Keyword = "constant"
	KwElsif    Keyword = "elsif"
	KwElseif   Keyword = "elseif"
	KwLoop     Keyword = "loop"
	KwExit     Keyword = "exit"
	KwWhile    Keyword = "while"
	KwContinue Keyword = "continue"
	KwReverse  Keyword = "reverse"
	KwPerform  Keyword = "perform"

	// Stage B (procedure follow-up) keywords for M0015.
	// These enable CREATE PROCEDURE and CALL statements.
	KwProcedure Keyword = "procedure"
	KwCall      Keyword = "call"
	KwOut       Keyword = "out"
	KwInout     Keyword = "inout"
	KwVariadic  Keyword = "variadic"

	// M0050-0004: savepoint keywords.
	KwSavepoint Keyword = "savepoint"
	KwRelease   Keyword = "release"

	// M0095-0004: VACUUM parenthesized syntax + vacuumdb catalog support.
	KwFreeze   Keyword = "freeze"
	KwParallel Keyword = "parallel"
	KwLateral  Keyword = "lateral"
	KwAny      Keyword = "any"
	// M0122-0004: SOME is a synonym for ANY in scalar-array comparisons
	// (`expr op SOME (...)`); accepted everywhere KwAny is.
	KwSome Keyword = "some"

	// M0095-0005: REINDEX statement stub.
	KwReindex Keyword = "reindex"

	// M0095-0008: CLUSTER statement stub.
	KwCluster Keyword = "cluster"

	// M0096-0006: PREPARE / EXECUTE statements and DROP INDEX CONCURRENTLY.
	KwPrepare  Keyword = "prepare"
	KwExecute  Keyword = "execute"
	KwDeallocate Keyword = "deallocate"

	// M0096-0010: MERGE INTO statement.
	KwMerge   Keyword = "merge"
	KwMatched Keyword = "matched"

	// M0096-0012: CREATE/DROP TRIGGER.
	KwTrigger Keyword = "trigger"

	// M0097-0014: CHECK constraints.
	KwCheck Keyword = "check"

	// M0134-0112: postfix `expr ISNULL` / `expr NOTNULL` — historical
	// PostgreSQL synonyms for `expr IS [NOT] NULL` (kwlist.h: both
	// TYPE_FUNC_NAME_KEYWORD, gram.y a_expr ISNULL / a_expr NOTNULL).
	KwIsnull  Keyword = "isnull"
	KwNotnull Keyword = "notnull"
)

// keywords lists every keyword v0 recognises. Lookup is via the
// lower-cased identifier text.
var keywords = map[string]Keyword{
	"begin":       KwBegin,
	"commit":      KwCommit,
	"rollback":    KwRollback,
	"abort":       KwAbort,
	"end":         KwEnd,
	"transaction": KwTransaction,
	"work":        KwWork,
	"vacuum":      KwVacuum,
	"analyze":     KwAnalyze,
	"analyse":     KwAnalyse,
	"verbose":     KwVerbose,
	"show":        KwShow,
	"set":         KwSet,
	"local":       KwLocal,
	"session":     KwSession,
	"to":          KwTo,
	"reset":       KwReset,
	"all":         KwAll,
	"default":     KwDefault,
	"select":      KwSelect,
	"distinct":    KwDistinct,
	"from":        KwFrom,
	"where":       KwWhere,
	"as":          KwAs,
	"order":       KwOrder,
	"by":          KwBy,
	"group":       KwGroup,
	"having":      KwHaving,
	"asc":         KwAsc,
	"desc":        KwDesc,
	"limit":       KwLimit,
	"offset":      KwOffset,
	"join":        KwJoin,
	"inner":       KwInner,
	"left":        KwLeft,
	"right":       KwRight,
	"full":        KwFull,
	"cross":       KwCross,
	"natural":     KwNatural,
	"outer":       KwOuter,
	"union":       KwUnion,
	"intersect":   KwIntersect,
	"except":      KwExcept,
	"and":         KwAnd,
	"or":          KwOr,
	"not":         KwNot,
	"null":        KwNull,
	"true":        KwTrue,
	"false":       KwFalse,
	"is":          KwIs,
	"insert":      KwInsert,
	"into":        KwInto,
	"values":      KwValues,
	"update":      KwUpdate,
	"delete":      KwDelete,
	"returning":   KwReturning,
	"create":      KwCreate,
	"table":       KwTable,
	"index":       KwIndex,
	"unique":      KwUnique,
	"on":          KwOn,
	"using":       KwUsing,
	"unlogged":    KwUnlogged,
	"drop":        KwDrop,
	"if":          KwIf,
	"exists":      KwExists,
	"truncate":    KwTruncate,
	"with":        KwWith,
	"recursive":   KwRecursive,
	"tablespace":  KwTablespace,
	"primary":     KwPrimary,
	"key":         KwKey,
	"cascade":     KwCascade,
	"restrict":    KwRestrict,
	"alter":       KwAlter,
	"add":         KwAdd,
	"constraint":  KwConstraint,
	"column":      KwColumn,
	"foreign":     KwForeign,
	"references":  KwReferences,
	"deferrable":  KwDeferrable,
	"case":        KwCase,
	"when":        KwWhen,
	"then":        KwThen,
	"else":        KwElse,
	"explain":     KwExplain,
	"in":          KwIn,
	"view":        KwView,
	"replace":     KwReplace,
	"copy":        KwCopy,
	"checkpoint":  KwCheckpoint,
	"like":         KwLike,
	"ilike":        KwIlike,
	"escape":       KwEscape,
	"similar":      KwSimilar,
	"between":      KwBetween,
	"symmetric":    KwSymmetric,
	"asymmetric":   KwAsymmetric,
	"publication":  KwPublication,
	"subscription": KwSubscription,
	"connection":   KwConnection,
	"for":          KwFor,
	"tables":       KwTables,
	"conflict":     KwConflict,
	"do":           KwDo,
	"nothing":      KwNothing,
	"share":        KwShare,
	"of":           KwOf,
	"nowait":       KwNowait,
	"skip":         KwSkip,
	"locked":       KwLocked,
	"over":         KwOver,
	"partition":    KwPartition,
	"function":     KwFunction,
	"returns":      KwReturns,
	"language":     KwLanguage,
	"return":       KwReturn,
	"declare":      KwDeclare,
	"constant":     KwConstant,
	"elsif":        KwElsif,
	"elseif":       KwElseif,
	"loop":         KwLoop,
	"exit":         KwExit,
	"while":        KwWhile,
	"continue":     KwContinue,
	"reverse":      KwReverse,
	"perform":      KwPerform,
	"procedure":    KwProcedure,
	"call":         KwCall,
	"out":          KwOut,
	"inout":        KwInout,
	"variadic":     KwVariadic,
	"savepoint":    KwSavepoint,
	"release":      KwRelease,
	"freeze":       KwFreeze,
	"parallel":     KwParallel,
	"lateral":      KwLateral,
	"any":          KwAny,
	"some":         KwSome,
	"reindex":      KwReindex,
	"cluster":      KwCluster,
	"prepare":      KwPrepare,
	"execute":      KwExecute,
	"deallocate":   KwDeallocate,
	"merge":        KwMerge,
	"matched":      KwMatched,
	"trigger":      KwTrigger,
	"check":        KwCheck,
	"isnull":       KwIsnull,
	"notnull":      KwNotnull,
}

// Token is one lexer output. Value is the source bytes (lower-cased
// for keywords/idents, raw payload for literals). Pos is the byte
// offset in the original input.
type Token struct {
	Kind    TokenKind
	Keyword Keyword // populated when Kind == TokenKeyword
	Value   string
	Pos     int
}
