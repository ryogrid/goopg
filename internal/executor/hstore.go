package executor

// hstore_in / hstore_out (postgres/contrib/hstore/hstore_io.c) for the
// hstore contrib type, which CREATE EXTENSION hstore installs
// (knownExtensions). goopg carries an hstore value as its canonical text:
// hstore_in parses the input (parse_hstore / get_val), sorts the pairs by
// key length then key bytes and keeps one pair per key (comparePairs,
// hstoreUniquePairs), and hstore_out renders `"k"=>"v"` pairs joined by
// ", ", a NULL value as an unquoted NULL. Before M0149-0046f CREATE
// EXTENSION hstore failed, so subscription/t/002_types.pl could not create
// its tst_hstore table on a goopg node.

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

type hstorePair struct {
	key, val string
	isNull   bool
}

// isHstoreTypeName reports whether typeName names the hstore type (bare or
// schema-qualified).
func isHstoreTypeName(typeName string) bool {
	return compositeFieldBaseTypeName(typeName) == "hstore"
}

// hstoreIsSpace is scanner_isspace.
func hstoreIsSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

type hstoreParser struct {
	s   string
	ptr int
}

func (p *hstoreParser) at() byte {
	if p.ptr >= len(p.s) {
		return 0
	}
	return p.s[p.ptr]
}

// syntaxError is prssyntaxerror: the character at the cursor and its
// 0-based byte position.
func (p *hstoreParser) syntaxError(pos int) error {
	_, n := utf8.DecodeRuneInString(p.s[p.ptr:])
	if n == 0 {
		n = 1
	}
	return &ExecError{Code: "42601", Pos: pos,
		Message: fmt.Sprintf("syntax error in hstore, near %q at position %d", p.s[p.ptr:p.ptr+n], p.ptr)}
}

func hstoreEOF(pos int) error {
	return &ExecError{Code: "42601", Pos: pos, Message: "syntax error in hstore: unexpected end of string"}
}

// getVal is get_val: one key (ignoreEq false) or value (ignoreEq true).
// ok=false is end of input before any word started.
func (p *hstoreParser) getVal(ignoreEq bool, pos int) (word string, escaped, ok bool, err error) {
	const (
		waitVal = iota
		inVal
		inEscVal
		waitEscIn
		waitEscEscIn
	)
	st := waitVal
	var b strings.Builder
	for {
		c := p.at()
		switch st {
		case waitVal:
			switch {
			case c == '"':
				escaped = true
				st = inEscVal
			case c == 0:
				return "", false, false, nil
			case c == '=' && !ignoreEq:
				return "", false, false, p.syntaxError(pos)
			case c == '\\':
				st = waitEscIn
			case !hstoreIsSpace(c):
				b.WriteByte(c)
				st = inVal
			}
		case inVal:
			switch {
			case c == '\\':
				st = waitEscIn
			case c == '=' && !ignoreEq:
				p.ptr--
				return b.String(), escaped, true, nil
			case c == ',' && ignoreEq:
				p.ptr--
				return b.String(), escaped, true, nil
			case hstoreIsSpace(c):
				return b.String(), escaped, true, nil
			case c == 0:
				p.ptr--
				return b.String(), escaped, true, nil
			default:
				b.WriteByte(c)
			}
		case inEscVal:
			switch {
			case c == '\\':
				st = waitEscEscIn
			case c == '"':
				return b.String(), escaped, true, nil
			case c == 0:
				return "", false, false, hstoreEOF(pos)
			default:
				b.WriteByte(c)
			}
		case waitEscIn:
			if c == 0 {
				return "", false, false, hstoreEOF(pos)
			}
			b.WriteByte(c)
			st = inVal
		case waitEscEscIn:
			if c == 0 {
				return "", false, false, hstoreEOF(pos)
			}
			b.WriteByte(c)
			st = inEscVal
		}
		p.ptr++
	}
}

// parseHstore is parse_hstore.
func parseHstore(s string, pos int) ([]hstorePair, error) {
	const (
		wKey = iota
		wVal
		wEq
		wGt
		wDel
	)
	p := &hstoreParser{s: s}
	var pairs []hstorePair
	st := wKey
	for {
		c := p.at()
		switch st {
		case wKey:
			key, _, ok, err := p.getVal(false, pos)
			if err != nil {
				return nil, err
			}
			if !ok {
				return pairs, nil
			}
			pairs = append(pairs, hstorePair{key: key})
			st = wEq
		case wEq:
			switch {
			case c == '=':
				st = wGt
			case c == 0:
				return nil, hstoreEOF(pos)
			case !hstoreIsSpace(c):
				return nil, p.syntaxError(pos)
			}
		case wGt:
			switch {
			case c == '>':
				st = wVal
			case c == 0:
				return nil, hstoreEOF(pos)
			default:
				return nil, p.syntaxError(pos)
			}
		case wVal:
			val, escaped, ok, err := p.getVal(true, pos)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, hstoreEOF(pos)
			}
			last := &pairs[len(pairs)-1]
			last.val = val
			last.isNull = !escaped && strings.EqualFold(val, "null")
			st = wDel
		case wDel:
			switch {
			case c == ',':
				st = wKey
			case c == 0:
				return pairs, nil
			case !hstoreIsSpace(c):
				return nil, p.syntaxError(pos)
			}
		}
		// at() reads past the end as the C string's terminating NUL, which
		// every state handles (returns or errors) — the loop cannot run on.
		p.ptr++
	}
}

// hstoreIn is hstore_in followed by hstore_out: the canonical text of s.
func hstoreIn(s string, pos int) (string, error) {
	pairs, err := parseHstore(s, pos)
	if err != nil {
		return "", err
	}
	// comparePairs: key length, then key bytes. All parsed pairs carry
	// needfree, so the tie is the input order — a stable sort keeps the
	// first occurrence of a duplicate key (hstoreUniquePairs), as PG 18.3
	// does for 'a=>1, a=>3'.
	sort.SliceStable(pairs, func(i, j int) bool {
		if len(pairs[i].key) != len(pairs[j].key) {
			return len(pairs[i].key) < len(pairs[j].key)
		}
		return pairs[i].key < pairs[j].key
	})
	var b strings.Builder
	n := 0
	for i, pr := range pairs {
		if i > 0 && pr.key == pairs[i-1].key {
			continue
		}
		if n > 0 {
			b.WriteString(", ")
		}
		n++
		b.WriteByte('"')
		hstoreCpw(&b, pr.key)
		b.WriteString(`"=>`)
		if pr.isNull {
			b.WriteString("NULL")
			continue
		}
		b.WriteByte('"')
		hstoreCpw(&b, pr.val)
		b.WriteByte('"')
	}
	return b.String(), nil
}

// hstoreCpw is cpw: backslash-escape '"' and '\'.
func hstoreCpw(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
}
