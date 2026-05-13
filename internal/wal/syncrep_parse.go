// Parser for the `synchronous_standby_names` GUC value (M0102-0005).
//
// Grammar (mirrors upstream guc.c / syncrep_gram.y, simplified subset):
//
//	value          := empty
//	                | name_list
//	                | quorum '(' name_list ')'
//	                | quorum num '(' name_list ')'
//	                | num '(' name_list ')'           # legacy FIRST n
//	quorum         := 'FIRST' | 'ANY'
//	name_list      := name (',' name)*
//	name           := bare_identifier | quoted_identifier
//
// Names are application_name strings; `"foo"` lets a name contain a comma or
// be a reserved keyword. The bare form (`a, b, c`) is the upstream pre-9.6
// legacy that means "FIRST 1 (a, b, c)" — first available wins.
package wal

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// parseSyncRepRule turns a synchronous_standby_names GUC value into the
// internal rule structure. Empty/whitespace-only input parses successfully
// as the no-op rule. A malformed input returns a non-nil error and an empty
// rule; callers (SetStandbyNames) treat the failure as "disable sync rep"
// rather than "block all commits", matching upstream's guc check_hook.
func parseSyncRepRule(value string) (syncRepRule, error) {
	tokens, err := tokenizeSyncRep(value)
	if err != nil {
		return syncRepRule{}, err
	}
	if len(tokens) == 0 {
		return syncRepRule{}, nil
	}
	p := syncRepParser{toks: tokens}
	rule, err := p.parse()
	if err != nil {
		return syncRepRule{}, err
	}
	return rule, nil
}

type syncRepToken struct {
	val    string
	quoted bool
}

func tokenizeSyncRep(value string) ([]syncRepToken, error) {
	var out []syncRepToken
	i := 0
	for i < len(value) {
		c := value[i]
		switch {
		case unicode.IsSpace(rune(c)):
			i++
		case c == ',' || c == '(' || c == ')':
			out = append(out, syncRepToken{val: string(c)})
			i++
		case c == '"':
			end := i + 1
			for end < len(value) {
				if value[end] == '"' {
					if end+1 < len(value) && value[end+1] == '"' {
						end += 2
						continue
					}
					break
				}
				end++
			}
			if end >= len(value) {
				return nil, fmt.Errorf("synchronous_standby_names: unterminated quoted identifier")
			}
			inner := strings.ReplaceAll(value[i+1:end], `""`, `"`)
			out = append(out, syncRepToken{val: inner, quoted: true})
			i = end + 1
		default:
			end := i
			for end < len(value) {
				cc := value[end]
				if unicode.IsSpace(rune(cc)) || cc == ',' || cc == '(' || cc == ')' || cc == '"' {
					break
				}
				end++
			}
			out = append(out, syncRepToken{val: value[i:end]})
			i = end
		}
	}
	return out, nil
}

type syncRepParser struct {
	toks []syncRepToken
	pos  int
}

func (p *syncRepParser) peek() (syncRepToken, bool) {
	if p.pos >= len(p.toks) {
		return syncRepToken{}, false
	}
	return p.toks[p.pos], true
}

func (p *syncRepParser) consume() syncRepToken {
	t := p.toks[p.pos]
	p.pos++
	return t
}

// parse implements the grammar at the top of the file.
func (p *syncRepParser) parse() (syncRepRule, error) {
	first, _ := p.peek()
	upper := strings.ToUpper(first.val)
	switch {
	case !first.quoted && (upper == "FIRST" || upper == "ANY"):
		mode := syncRepRuleFirst
		if upper == "ANY" {
			mode = syncRepRuleAny
		}
		p.consume()
		count, names, err := p.parseQuorumBody()
		if err != nil {
			return syncRepRule{}, err
		}
		if count > len(names) {
			return syncRepRule{}, fmt.Errorf(
				"synchronous_standby_names: requested %d, only %d names listed", count, len(names))
		}
		return syncRepRule{mode: mode, count: count, names: names}, nil

	case !first.quoted && isDecimal(first.val):
		// Legacy form: `n (a, b, c)` means FIRST n.
		count, err := strconv.Atoi(first.val)
		if err != nil || count < 1 {
			return syncRepRule{}, fmt.Errorf("synchronous_standby_names: invalid count %q", first.val)
		}
		p.consume()
		names, err := p.parseParenNameList()
		if err != nil {
			return syncRepRule{}, err
		}
		if count > len(names) {
			return syncRepRule{}, fmt.Errorf(
				"synchronous_standby_names: requested %d, only %d names listed", count, len(names))
		}
		return syncRepRule{mode: syncRepRuleFirst, count: count, names: names}, nil

	default:
		// Bare name list (no FIRST/ANY/n prefix) — upstream pre-9.6 form
		// equivalent to FIRST 1 (...).
		names, err := p.parseBareNameList()
		if err != nil {
			return syncRepRule{}, err
		}
		if len(names) == 0 {
			return syncRepRule{}, fmt.Errorf("synchronous_standby_names: empty name list")
		}
		return syncRepRule{mode: syncRepRuleFirst, count: 1, names: names}, nil
	}
}

// parseQuorumBody parses `[count] '(' name_list ')'`. The count is optional;
// when omitted upstream defaults to 1.
func (p *syncRepParser) parseQuorumBody() (int, []string, error) {
	count := 1
	if t, ok := p.peek(); ok && !t.quoted && isDecimal(t.val) {
		n, err := strconv.Atoi(t.val)
		if err != nil || n < 1 {
			return 0, nil, fmt.Errorf("synchronous_standby_names: invalid count %q", t.val)
		}
		count = n
		p.consume()
	}
	names, err := p.parseParenNameList()
	if err != nil {
		return 0, nil, err
	}
	return count, names, nil
}

func (p *syncRepParser) parseParenNameList() ([]string, error) {
	t, ok := p.peek()
	if !ok || t.val != "(" {
		return nil, fmt.Errorf("synchronous_standby_names: expected '(' before name list")
	}
	p.consume()
	var names []string
	for {
		nameTok, ok := p.peek()
		if !ok {
			return nil, fmt.Errorf("synchronous_standby_names: unterminated name list")
		}
		if nameTok.val == ")" {
			p.consume()
			break
		}
		if nameTok.val == "," {
			return nil, fmt.Errorf("synchronous_standby_names: unexpected ','")
		}
		names = append(names, nameTok.val)
		p.consume()
		// Optional comma between names.
		if sep, ok := p.peek(); ok && sep.val == "," {
			p.consume()
		}
	}
	return names, nil
}

func (p *syncRepParser) parseBareNameList() ([]string, error) {
	var names []string
	for {
		t, ok := p.peek()
		if !ok {
			break
		}
		if t.val == "," {
			return nil, fmt.Errorf("synchronous_standby_names: unexpected ','")
		}
		if t.val == "(" || t.val == ")" {
			return nil, fmt.Errorf("synchronous_standby_names: unexpected %q in bare name list", t.val)
		}
		names = append(names, t.val)
		p.consume()
		if sep, ok := p.peek(); ok && sep.val == "," {
			p.consume()
		}
	}
	return names, nil
}

func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
