package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// Pattern selectivity for LIKE / ILIKE restriction clauses — take2 P1-14b,
// the `patternsel` slice (regex operators remain open, see below).
//
// Upstream: postgres/src/backend/utils/adt/like_support.c
// (patternsel_common) with constants from
// postgres/src/include/utils/selfuncs.h. A `col LIKE pattern` clause with
// no estimator fell through to defaultGenericSelectivity (1/3); TPC-H Q9's
// `part.p_name LIKE '%green%'` priced `part` at 66,666 rows against ~10.6k
// actual (PG `patternsel`: 6,061), a 6.25x overestimate that propagated
// linearly into every joinrel above it (take2 TODO P1-16).
//
// The port follows upstream's structure: an exact (wildcard-free) pattern
// delegates to the equality estimator; otherwise MCV entries are matched
// exactly and histogram bounds are used as a representative sample, with a
// heuristic blend for small histograms. Three deliberate scope cuts, each
// recorded where it bites:
//   - regex (`~`, `~*`, `!~`, `!~*`) operators are NOT routed here (P1-14b
//     remainder): their remainder heuristic (regex_selectivity) and match
//     semantics differ, and no measured query needs them yet.
//   - the small-histogram heuristic's prefix-range half reuses the existing
//     inequality estimator, which still returns its documented 0.5 fallback
//     for text (P1-11b open half). It only weights (1 - hist_size/100) of
//     small-histogram cases; the measured Q9 shape (histogram >= 100) never
//     touches it.
//   - multibyte case-folding uses strings.ToLower on both sides (ASCII
//     approximation); upstream folds per the pattern operator's collation.

// defaultMatchSelectivity is upstream's DEFAULT_MATCH_SEL
// (selfuncs.h:46): the no-statistics fallback for a pattern match, and
// 1-that for the negated operators.
const defaultMatchSelectivity = 0.005

// patternOpDirs splits the four routed opcodes into case handling and
// negation. Anything else is not a pattern operator.
func patternOpDirs(op parser.OpCode) (icase, negate bool, ok bool) {
	switch op {
	case parser.OpLike:
		return false, false, true
	case parser.OpILike:
		return true, false, true
	case parser.OpNotLike:
		return false, true, true
	case parser.OpNotILike:
		return true, true, true
	}
	return false, false, false
}

// matchLikePattern reports whether value matches a LIKE pattern (% and _
// wildcards, backslash escapes). A port of the core loop of PG's MatchText
// (like_match.c) restricted to LIKE metacharacters — sufficient for an
// estimator, which applies it to statistics strings, never to table data.
func matchLikePattern(value, pattern string, icase bool) bool {
	if icase {
		value = strings.ToLower(value)
		pattern = strings.ToLower(pattern)
	}
	vr, pr := []rune(value), []rune(pattern)
	return matchLikeRunes(vr, pr)
}

func matchLikeRunes(v, p []rune) bool {
	vi, pi := 0, 0
	star, match := -1, 0
	for vi < len(v) {
		if pi < len(p) {
			switch p[pi] {
			case '\\':
				if pi+1 < len(p) {
					pi++
					if v[vi] == p[pi] {
						vi++
						pi++
						continue
					}
					if !backtrackLike(v, p, &vi, &pi, star, &match) {
						return false
					}
					continue
				}
				// Trailing backslash matches a literal backslash.
				if v[vi] == '\\' {
					vi++
					pi++
					continue
				}
				if !backtrackLike(v, p, &vi, &pi, star, &match) {
					return false
				}
				continue
			case '_':
				vi++
				pi++
				continue
			case '%':
				star = pi
				match = vi
				pi++
				continue
			default:
				if v[vi] == p[pi] {
					vi++
					pi++
					continue
				}
				if !backtrackLike(v, p, &vi, &pi, star, &match) {
					return false
				}
				continue
			}
		}
		if !backtrackLike(v, p, &vi, &pi, star, &match) {
			return false
		}
	}
	for pi < len(p) && p[pi] == '%' {
		pi++
	}
	return pi == len(p)
}

func backtrackLike(v, p []rune, vi, pi *int, star int, match *int) bool {
	if star == -1 {
		return false
	}
	*match++
	*vi = *match
	*pi = star + 1
	return *vi <= len(v)
}

// likeSelectivityHeuristic ports PG's like_selectivity (like_support.c):
// the structure-driven estimate for a pattern remainder — FIXED_CHAR_SEL
// per literal, ANY_CHAR_SEL per `_`, FULL_WILDCARD_SEL per `%`, capped at
// 1.0. Used only inside the small-histogram blend below.
func likeSelectivityHeuristic(pattern string) float64 {
	const (
		fixedCharSel = 0.20
		anyCharSel   = 0.9
		fullWildSel  = 5.0
	)
	sel := 1.0
	pr := []rune(pattern)
	for i := 0; i < len(pr); i++ {
		switch pr[i] {
		case '%':
			sel *= fullWildSel
		case '_':
			sel *= anyCharSel
		case '\\':
			// A backslash quotes the next character (or is a literal
			// trailing backslash); either way one fixed character.
			i++
			sel *= fixedCharSel
		default:
			sel *= fixedCharSel
		}
	}
	if sel > 1.0 {
		sel = 1.0
	}
	return sel
}

// patternSelectivity estimates `col LIKE|ILIKE pattern` (or its negation)
// against the column's statistics, mirroring patternsel_common. It resolves
// statistics itself (like eqOpSelectivity) so callers pass the plan context,
// not pre-resolved inputs.
func patternSelectivity(col *ColumnRef, pattern string, icase, negate bool, child Node) float64 {
	fallback := defaultMatchSelectivity
	if negate {
		fallback = 1 - defaultMatchSelectivity
	}
	if col == nil {
		return fallback
	}
	stats := columnStatsForChild(col.Index, child)
	tuples := columnRawRowsForChild(col.Index, child)
	if stats == nil {
		return fallback
	}
	// Exact (wildcard-free) pattern: estimate as `=` (patternsel_common's
	// Pattern_Prefix_Exact arm delegates to var_eq_const).
	if _, exact, ok := ExtractLikePrefix(pattern); ok && exact {
		return eqSelectivityForColumn(stats, &StringConst{Value: pattern}, tuples)
	}
	nullfrac := stats.NullFrac
	// MCV arm: exact pattern match per entry (MCVs are not in the
	// histogram population, so their mass adds directly).
	mcvSelec, sumcommon := 0.0, 0.0
	for _, m := range stats.MCV {
		sumcommon += m.Frequency
		if matchLikePattern(m.Value, pattern, icase) {
			mcvSelec += m.Frequency
		}
	}
	// Histogram arm: fraction of bounds matching, skipping the first and
	// last (PG's n_skip=1 outlier guard). Bounds below the minimum size
	// are not representative (min_hist_size=10).
	bounds := stats.Histogram
	selec := -1.0
	histSize := len(bounds)
	if histSize >= 10 {
		nmatch := 0
		for _, b := range bounds[1 : histSize-1] {
			if matchLikePattern(b, pattern, icase) {
				nmatch++
			}
		}
		selec = float64(nmatch) / float64(histSize-2)
		if histSize < 100 {
			// Small histogram: blend with the heuristic, trusting the
			// sample increasingly with size (hist_weight = size/100):
			// heursel = prefixsel * rest_selec (patternsel_common).
			// The prefix half reuses the range estimator over
			// [prefix, successor); its text-scalar limitation is
			// documented at the file header. The remainder half is
			// like_selectivity on the pattern past the fixed prefix.
			prefixsel := 1.0
			rest := pattern
			if prefix, exact, ok := ExtractLikePrefix(pattern); ok && !exact && prefix != "" {
				rest = pattern[len(prefix):]
				if succ, ok := IncrementString(prefix); ok {
					ref := &ColumnRef{Index: col.Index, Name: col.Name, Type: col.Type}
					prefixsel = conjunctionSelectivity([]Expr{
						&BinaryOp{Op: parser.OpGe, Left: ref, Right: &StringConst{Value: prefix}},
						&BinaryOp{Op: parser.OpLt, Left: &ColumnRef{Index: col.Index, Name: col.Name, Type: col.Type}, Right: &StringConst{Value: succ}},
					}, child)
				}
			}
			heursel := prefixsel * likeSelectivityHeuristic(rest)
			w := float64(histSize) / 100.0
			selec = selec*w + heursel*(1.0-w)
		}
	}
	if selec < 0 {
		// No usable histogram: the heuristic alone.
		selec = likeSelectivityHeuristic(pattern)
	}
	// Disbelieve exact 0/1 from the sample; the MCV merge below re-adds
	// the exactly-known mass.
	if selec < 0.0001 {
		selec = 0.0001
	} else if selec > 0.9999 {
		selec = 0.9999
	}
	// Merge: the histogram covers only non-null, non-MCV rows.
	selec = selec*(1.0-nullfrac-sumcommon) + mcvSelec
	result := selec
	if negate {
		result = 1.0 - result - nullfrac
	}
	return clampProbability(result)
}

// patternClauseSelectivity routes a LIKE-family BinaryOp's operands into
// patternSelectivity. Non-(column, const-pattern) shapes decline to the
// match default, as patternsel_common punts non-var-op-const to it; a NULL
// pattern is strict (never true, even negated).
func patternClauseSelectivity(op parser.OpCode, left, right Expr, child Node) float64 {
	icase, negate, ok := patternOpDirs(op)
	if !ok {
		return defaultMatchSelectivity
	}
	fallback := defaultMatchSelectivity
	if negate {
		fallback = 1 - defaultMatchSelectivity
	}
	// NULL on either side: LIKE is strict (never true, even negated).
	// Checked before shape normalisation, which declines NULL consts.
	if _, isNull := left.(*NullConst); isNull {
		return 0.0
	}
	if _, isNull := right.(*NullConst); isNull {
		return 0.0
	}
	col, val, ok := normalizeColumnConst(left, right)
	if !ok {
		return fallback
	}
	pattern, ok := patternConstString(val)
	if !ok || pattern == "" {
		return fallback
	}
	return patternSelectivity(col, pattern, icase, negate, child)
}

// patternClauseSelectivityWithSource is the reliability-tracking twin.
// Reliable iff a column-LIKE-const shape matched AND the column has
// statistics — the same rule as the equality twin. (Before this port the
// twin marked every LIKE unreliable, so the search's `initialRelRows`
// priced TPC-H Q9's `part` side at the full 200k even where the legacy arm
// had begun to know better.)
func patternClauseSelectivityWithSource(op parser.OpCode, left, right Expr, child Node) selectivityEstimate {
	icase, negate, ok := patternOpDirs(op)
	unreliable := func() selectivityEstimate {
		if negate {
			return selectivityEstimate{value: 1 - defaultMatchSelectivity, reliable: false}
		}
		return selectivityEstimate{value: defaultMatchSelectivity, reliable: false}
	}
	if !ok {
		return unreliable()
	}
	// NULL on either side: LIKE is strict (never true, even negated).
	// Checked before shape normalisation, which declines NULL consts.
	if _, isNull := left.(*NullConst); isNull {
		return selectivityEstimate{value: 0.0, reliable: true}
	}
	if _, isNull := right.(*NullConst); isNull {
		return selectivityEstimate{value: 0.0, reliable: true}
	}
	col, val, ok := normalizeColumnConst(left, right)
	if !ok {
		return unreliable()
	}
	pattern, ok := patternConstString(val)
	if !ok || pattern == "" {
		return unreliable()
	}
	stats := columnStatsForChild(col.Index, child)
	if stats == nil {
		return unreliable()
	}
	return selectivityEstimate{value: patternSelectivity(col, pattern, icase, negate, child), reliable: true}
}

// patternConstString extracts the match pattern from the const side of a
// LIKE clause: a bare string constant, or a LikeEscapePattern wrapper
// (LIKE...ESCAPE) whose escape is itself a usable single character.
// Anything else declines — PG punts non-Const the same way.
func patternConstString(val Expr) (string, bool) {
	switch v := val.(type) {
	case *StringConst:
		return v.Value, true
	case *LikeEscapePattern:
		pat, ok := patternConstString(v.Pattern)
		if !ok {
			return "", false
		}
		// An explicit escape is honoured only when it is a usable single
		// character; anything fancier declines rather than misreads.
		// (The executor rewrites patterns into backslash convention
		// before matching; the estimator below assumes that convention.)
		if v.Escape == nil {
			return pat, true
		}
		if esc, ok := v.Escape.(*StringConst); ok {
			if len([]rune(esc.Value)) == 1 {
				return pat, true
			}
		}
		return "", false
	}
	return "", false
}
