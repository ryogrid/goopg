package sqlparser

// Resolution tables joining three worlds at init time:
//   keywords_gen.go  (name strings from kwlist.h),
//   grammar/*.y      (terminal names via goyacc's yyToknames),
//   the legacy lexer (Token kinds / operator strings).
//
// goyacc terminal-value contract (verified against generated yylex1/yyTok1,
// 2026-08-25):
//   - NAMED terminals: Lex returns the DECLARED sequential number; yylex1
//     maps it through yyTok3.
//   - CHAR-LITERAL terminals (';' '(' ...): Lex returns the ASCII CODE;
//     yylex1 maps it through yyTok1[ascii]. Sequential numbers are wrong
//     here even though yyToknames prints "'c'" entries for them.
//   - EOF: return <= 0 (yylex1 substitutes yyTok1[0] == $end).

// namedOperator maps generic operator STRINGS produced by the legacy lexer
// onto scan.l's distinct multi-char terminals (05-risks #11). Everything not
// listed here is the generic Op terminal.
var namedOperator = map[string]string{
	"::": "TYPECAST",
	"..": "DOT_DOT",
	":=": "COLON_EQUALS",
	"=>": "EQUALS_GREATER",
	"<=": "LESS_EQUALS",
	">=": "GREATER_EQUALS",
	"<>": "NOT_EQUALS",
	"!=": "NOT_EQUALS",
}

// keywordByText indexes keywordDefs by lowercase keyword text.
var keywordByText = func() map[string]keywordDef {
	m := make(map[string]keywordDef, len(keywordDefs))
	for _, d := range keywordDefs {
		m[d.Text] = d
	}
	return m
}()

var (
	nameToNum       = make(map[string]int, len(genTokenNums))
	keywordTokenNum = make(map[string]int, len(keywordDefs))

	// unresolved records terminal names referenced by our tables but absent
	// from the generated grammar — must stay empty (asserted by tests).
	unresolved []string

	yyUnkCode = 3 // "$unk" printing index; asserted at init below
)

func init() {
	for name, num := range genTokenNums {
		nameToNum[name] = num
	}
	delete(nameToNum, "yyEofCode")
	for _, d := range keywordDefs {
		if num, ok := nameToNum[d.Token]; ok {
			keywordTokenNum[d.Token] = num
		} else {
			unresolved = append(unresolved, d.Token)
		}
	}
	for _, name := range []string{
		"IDENT", "FCONST", "SCONST", "BCONST", "ICONST", "PARAM", "Op",
		"TYPECAST", "DOT_DOT", "COLON_EQUALS", "EQUALS_GREATER",
		"LESS_EQUALS", "GREATER_EQUALS", "NOT_EQUALS",
	} {
		if _, ok := nameToNum[name]; !ok {
			unresolved = append(unresolved, name)
		}
	}
	initSubstRules()
}

// resolve returns the token number for a terminal NAME, falling back to $unk
// (which yields a clean syntax error rather than table corruption).
func resolve(name string) int {
	if n, ok := nameToNum[name]; ok {
		return n
	}
	return yyUnkCode
}
