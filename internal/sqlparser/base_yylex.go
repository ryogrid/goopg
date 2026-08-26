package sqlparser

// Port of PostgreSQL's base_yylex() lookahead filter
// (postgres/src/backend/parser/parser.c:111-251). Upstream substitutes _LA
// token variants when the NEXT token is in a fixed follower set, so the
// grammar can disambiguate clause-leading WITH/NOT/NULLS from their other
// uses. Keyword-category handling is NOT lexical (02-grammar-porting §4).
//
// The UIDENT/USCONST → UESCAPE triple branch is stubbed out until our lexer
// grows unicode-identifier terminals (05-risks #4 fixture list, P2).

type substRule struct {
	cur       int // base terminal (e.g. WITH)
	subst     int // terminal substituted when a follower matches (WITH_LA)
	followers map[int]bool
}

var substRules []substRule

func initSubstRules() {
	pairs := []struct{ cur, la string }{
		{"FORMAT", "FORMAT_LA"},
		{"VALUES", "VALUES_LA"},
		{"NOT", "NOT_LA"},
		{"NULLS_P", "NULLS_LA"},
		{"WITH", "WITH_LA"},
		{"WITHOUT", "WITHOUT_LA"},
	}
	groups := map[string][]string{
		"FORMAT":  {"JSON"},
		"VALUES": {"("},
		// EXACTLY upstream's set (parser.c base_yylex: BETWEEN, IN_P, LIKE,
		// ILIKE, SIMILAR). EXISTS was here by mistake: it made `NOT EXISTS`
		// lex as NOT_LA EXISTS, and NOT_LA only appears in the
		// `a_expr NOT_LA BETWEEN|IN|LIKE...` alternatives, so every
		// `NOT EXISTS (...)` was a syntax error on the routed SELECT path
		// (TPC-H Q21 and Q22 among them).
		"NOT":     {"BETWEEN", "IN_P", "LIKE", "ILIKE", "SIMILAR"},
		"NULLS_P": {"FIRST_P", "LAST_P"},
		"WITH":    {"TIME", "ORDINALITY"},
		"WITHOUT": {"TIME"},
	}
	for _, p := range pairs {
		cur, okCur := nameToNum[p.cur]
		la, okLA := nameToNum[p.la]
		if !okCur || !okLA {
			// A grammar wave has not declared these terminals yet; skip the
			// rule (it becomes live automatically once declared).
			continue
		}
		followers := make(map[int]bool, len(groups[p.cur]))
		for _, f := range groups[p.cur] {
			n, ok := nameToNum[f]
			if !ok && len(f) == 1 {
				// Char-literal terminal: ASCII code per the yylex1 contract
				// (no named const exists for it in tokennums_gen.go).
				n, ok = int(f[0]), true
			}
			if ok {
				followers[n] = true
			}
		}
		substRules = append(substRules, substRule{cur: cur, subst: la, followers: followers})
	}
}

func substFor(cur int) (substRule, bool) {
	for _, r := range substRules {
		if r.cur == cur && cur != r.subst {
			return r, true
		}
	}
	return substRule{}, false
}
