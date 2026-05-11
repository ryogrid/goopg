package parser

// KeywordCategory classifies a keyword's role in the grammar, matching the
// four-way split from upstream's kwlist.h. The classification governs whether
// a keyword may appear unquoted as an identifier (column name, table name,
// alias, etc.) in positions where the parser would otherwise accept an ident.
//
// Upstream reference: postgres/src/include/parser/kwlist.h
type KeywordCategory int

const (
	// KwCatReserved keywords conflict structurally with the grammar and must
	// always be double-quoted when used as identifiers. Examples: SELECT,
	// FROM, WHERE, AND, OR, NULL, TRUE, FALSE.
	KwCatReserved KeywordCategory = iota

	// KwCatTypeFunc keywords are reserved only in type-name and
	// function-name positions; they can appear as column names and table
	// aliases. Examples: LIKE, ILIKE, VERBOSE, JOIN variants, BINARY.
	KwCatTypeFunc

	// KwCatColName keywords are usable as column names and table names but
	// are restricted in a handful of other contexts. Examples: VALUES,
	// EXISTS, BETWEEN, ROW, TIME.
	KwCatColName

	// KwCatUnreserved keywords may appear as identifiers in any context.
	// Examples: BEGIN, COMMIT, INDEX, KEY, FUNCTION, LANGUAGE, PARTITION.
	KwCatUnreserved
)

// keywordCategory maps every Keyword constant to its grammar category.
// Keywords absent from this map default to KwCatReserved (safe fallback).
//
// Mapping follows postgres/src/include/parser/kwlist.h; deviations are noted.
// goopg-specific keywords without an upstream equivalent are treated as
// KwCatUnreserved so they never block identifier usage.
var keywordCategory = map[Keyword]KeywordCategory{
	// ── Unreserved ──────────────────────────────────────────────────────
	KwBegin:        KwCatUnreserved,
	KwCommit:       KwCatUnreserved,
	KwRollback:     KwCatUnreserved,
	KwAbort:        KwCatUnreserved,
	KwTransaction:  KwCatUnreserved,
	KwWork:         KwCatUnreserved,
	KwVacuum:       KwCatUnreserved,
	KwShow:         KwCatUnreserved,
	KwSet:          KwCatUnreserved,
	KwLocal:        KwCatUnreserved,
	KwSession:      KwCatUnreserved,
	KwReset:        KwCatUnreserved,
	KwBy:           KwCatUnreserved,
	KwInsert:       KwCatUnreserved,
	KwUpdate:       KwCatUnreserved,
	KwDelete:       KwCatUnreserved,
	KwIndex:        KwCatUnreserved,
	KwDrop:         KwCatUnreserved,
	KwIf:           KwCatUnreserved,
	KwTruncate:     KwCatUnreserved,
	KwRecursive:    KwCatUnreserved,
	KwTablespace:   KwCatUnreserved,
	KwKey:          KwCatUnreserved,
	KwCascade:      KwCatUnreserved,
	KwRestrict:     KwCatUnreserved,
	KwAlter:        KwCatUnreserved,
	KwAdd:          KwCatUnreserved,
	KwDeferrable:   KwCatUnreserved,
	KwExplain:      KwCatUnreserved,
	KwView:         KwCatUnreserved,
	KwReplace:      KwCatUnreserved,
	KwCopy:         KwCatUnreserved,
	KwCheckpoint:   KwCatUnreserved,
	KwPublication:  KwCatUnreserved,
	KwSubscription: KwCatUnreserved,
	KwConnection:   KwCatUnreserved,
	KwTables:       KwCatUnreserved,
	KwConflict:     KwCatUnreserved,
	KwNothing:      KwCatUnreserved,
	KwShare:        KwCatUnreserved,
	KwNowait:       KwCatUnreserved,
	KwSkip:         KwCatUnreserved,
	KwLocked:       KwCatUnreserved,
	KwOver:         KwCatUnreserved,
	KwPartition:    KwCatUnreserved,
	KwFunction:     KwCatUnreserved,
	KwReturns:      KwCatUnreserved,
	KwLanguage:     KwCatUnreserved,
	KwReturn:       KwCatUnreserved,
	KwDeclare:      KwCatUnreserved,
	KwSavepoint:    KwCatUnreserved,
	KwRelease:      KwCatUnreserved,
	KwUnlogged:     KwCatUnreserved,
	KwProcedure:    KwCatUnreserved,
	KwCall:         KwCatUnreserved,
	KwFreeze:       KwCatUnreserved,
	KwAny:          KwCatUnreserved,
	KwParallel:     KwCatUnreserved,
	KwReindex:      KwCatUnreserved,
	KwCluster:      KwCatUnreserved,

	// goopg-specific (no upstream equivalent) — treated as unreserved.
	KwConstant: KwCatUnreserved,
	KwElsif:    KwCatUnreserved,
	KwElseif:   KwCatUnreserved,
	KwLoop:     KwCatUnreserved,
	KwExit:     KwCatUnreserved,
	KwWhile:    KwCatUnreserved,
	KwContinue: KwCatUnreserved,
	KwReverse:  KwCatUnreserved,
	KwPerform:  KwCatUnreserved,

	// ── col_name ────────────────────────────────────────────────────────
	KwValues:  KwCatColName,
	KwExists:  KwCatColName,
	KwBetween: KwCatColName,
	KwOut:     KwCatColName,
	KwInout:   KwCatColName,

	// ── type_func_name ──────────────────────────────────────────────────
	// These appear in type and function positions but are safe as column names.
	KwVerbose: KwCatTypeFunc,
	KwJoin:    KwCatTypeFunc,
	KwInner:   KwCatTypeFunc,
	KwLeft:    KwCatTypeFunc,
	KwRight:   KwCatTypeFunc,
	KwFull:    KwCatTypeFunc,
	KwCross:   KwCatTypeFunc,
	KwNatural: KwCatTypeFunc,
	KwOuter:   KwCatTypeFunc,
	KwIs:      KwCatTypeFunc,
	KwLike:    KwCatTypeFunc,
	KwLateral: KwCatTypeFunc,

	// ── reserved ────────────────────────────────────────────────────────
	// Explicitly listed for documentation; the map fallback would handle them
	// too, but listing them makes the category table self-contained.
	KwEnd:        KwCatReserved,
	KwAnalyze:    KwCatReserved,
	KwAnalyse:    KwCatReserved,
	KwTo:         KwCatReserved,
	KwAll:        KwCatReserved,
	KwDefault:    KwCatReserved,
	KwSelect:     KwCatReserved,
	KwDistinct:   KwCatReserved,
	KwFrom:       KwCatReserved,
	KwWhere:      KwCatReserved,
	KwAs:         KwCatReserved,
	KwOrder:      KwCatReserved,
	KwGroup:      KwCatReserved,
	KwHaving:     KwCatReserved,
	KwAsc:        KwCatReserved,
	KwDesc:       KwCatReserved,
	KwLimit:      KwCatReserved,
	KwOffset:     KwCatReserved,
	KwUnion:      KwCatReserved,
	KwIntersect:  KwCatReserved,
	KwExcept:     KwCatReserved,
	KwAnd:        KwCatReserved,
	KwOr:         KwCatReserved,
	KwNot:        KwCatReserved,
	KwNull:       KwCatReserved,
	KwTrue:       KwCatReserved,
	KwFalse:      KwCatReserved,
	KwInto:       KwCatReserved,
	KwReturning:  KwCatReserved,
	KwCreate:     KwCatReserved,
	KwTable:      KwCatReserved,
	KwUnique:     KwCatReserved,
	KwOn:         KwCatReserved,
	KwUsing:      KwCatReserved,
	KwWith:       KwCatReserved,
	KwPrimary:    KwCatReserved,
	KwConstraint: KwCatReserved,
	KwColumn:     KwCatReserved,
	KwForeign:    KwCatReserved,
	KwReferences: KwCatReserved,
	KwCase:       KwCatReserved,
	KwWhen:       KwCatReserved,
	KwThen:       KwCatReserved,
	KwElse:       KwCatReserved,
	KwFor:        KwCatReserved,
	KwIn:         KwCatReserved,
	KwDo:         KwCatReserved,
	KwVariadic:   KwCatReserved,
	KwOf:         KwCatReserved,
}

// IsColNameKeyword reports whether kw may appear unquoted as a column name,
// table name, or alias. Returns true for KwCatUnreserved, KwCatColName, and
// KwCatTypeFunc; returns false for KwCatReserved.
//
// Used by parseIdent() to decide whether to accept a keyword token as an
// identifier. This mirrors upstream's grammar productions that call
// col_name_keyword, type_func_name_keyword, or unreserved_keyword.
func IsColNameKeyword(kw Keyword) bool {
	cat, ok := keywordCategory[kw]
	if !ok {
		// Unknown keyword: safe fallback — treat as reserved to avoid
		// silently accepting a future reserved word. Any keyword added to
		// the token list should be explicitly placed in keywordCategory.
		return false
	}
	return cat != KwCatReserved
}
