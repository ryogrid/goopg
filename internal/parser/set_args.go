package parser

import (
	"math"
	"strconv"

	"github.com/goopg/goopg/internal/utils/misc"
)

// scanSetArgs reads a SET value list — gram.y var_list: var_value [, ...] —
// from toks[i:], producing the typed elements flatten_set_variable_args
// consumes. It stops at the first token that cannot continue the list and
// returns the index of that token; ok is false when no element was read or a
// ',' is not followed by an element.
//
// Element shapes (gram.y var_value / NumericOnly / opt_boolean_or_string):
//   - an integer literal that fits int32 is an Integer (printed as %d, so
//     `007` and `0x10` flatten to 7 and 16); a larger one is a Float kept as
//     written (scan.l process_integer_literal);
//   - any other numeric literal is a Float kept as written;
//   - a leading '-' negates the number, a leading '+' is dropped;
//   - a string literal, an identifier (downcased unless double-quoted) or a
//     keyword (TRUE/FALSE/ON and the non-reserved words) is a String.
func scanSetArgs(toks []Token, i int) (args []misc.SetArg, next int, ok bool) {
	for i < len(toks) {
		t := toks[i]
		switch t.Kind {
		case TokenIntLit, TokenNumericLit:
			args = append(args, numericSetArg(t, false))
			i++
		case TokenStringLit, TokenIdent, TokenQuotedIdent, TokenKeyword:
			args = append(args, misc.SetArg{Kind: misc.SetArgString, Val: t.Value})
			i++
		case TokenOperator:
			if (t.Value != "-" && t.Value != "+") || i+1 >= len(toks) ||
				(toks[i+1].Kind != TokenIntLit && toks[i+1].Kind != TokenNumericLit) {
				return args, i, false
			}
			args = append(args, numericSetArg(toks[i+1], t.Value == "-"))
			i += 2
		default:
			return args, i, false
		}
		if i < len(toks) && toks[i].Kind == TokenSymbol && toks[i].Value == "," {
			i++
			continue
		}
		return args, i, true
	}
	return args, i, false
}

// numericSetArg converts one numeric token, negated when neg is set.
func numericSetArg(t Token, neg bool) misc.SetArg {
	if t.Kind == TokenIntLit {
		if n, err := parseIntLiteral(t.Value); err == nil && n <= math.MaxInt32 {
			if neg {
				n = -n
			}
			return misc.SetArg{Kind: misc.SetArgInteger, Val: strconv.FormatInt(n, 10)}
		}
	}
	if neg {
		// doNegateFloat: the text is negated by prefixing a minus.
		return misc.SetArg{Kind: misc.SetArgFloat, Val: "-" + t.Value}
	}
	return misc.SetArg{Kind: misc.SetArgFloat, Val: t.Value}
}

// ParseSetArgList parses text that must be exactly a SET value list (an
// optional trailing ';' aside) into its typed elements. Callers that take a
// SET value apart from raw statement text — the simple-query SET fast path
// and ALTER DATABASE / ROLE ... SET — use it to flatten the value the way
// the grammar path does. ok is false for anything else, including a lone
// unquoted DEFAULT, which callers treat as a reset.
func ParseSetArgList(text string) ([]misc.SetArg, bool) {
	toks, err := Lex(text)
	if err != nil {
		return nil, false
	}
	if len(toks) > 0 && toks[0].Kind == TokenKeyword && toks[0].Keyword == KwDefault {
		return nil, false
	}
	args, i, ok := scanSetArgs(toks, 0)
	if !ok {
		return nil, false
	}
	if i < len(toks) && toks[i].Kind == TokenSymbol && toks[i].Value == ";" {
		i++
	}
	if i < len(toks) && toks[i].Kind != TokenEOF {
		return nil, false
	}
	return args, true
}
