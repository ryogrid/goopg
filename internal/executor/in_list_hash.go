package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// minHashedInListItems is PG's MIN_ARRAY_SIZE_FOR_HASHED_SAOP
// (optimizer/util/clauses.c): convert_saop_to_hashed_saop hashes a constant
// `= ANY` array of at least nine elements, and ExecEvalHashedScalarArrayOp
// then probes a hash table per row instead of comparing every element.
const minHashedInListItems = 9

// inListHash is a literal IN list's items as a hash set (M0122-0012 slice
// 50), the twin of PG's hashed ScalarArrayOp. It serves only the kinds where
// the plain-equality loop of evalInExprScalarRow decides a match by exact key
// equality — compareEq compares two strings with == and two ints with ==,
// with no error path — so a probe returns exactly what the loop returns:
// true on a match; else NULL if the list holds a NULL; else false (IN) or
// the negation (NOT IN). Any other pairing — a numeric or date operand, a
// string operand against int items, a list mixing kinds — keeps the loop.
type inListHash struct {
	built bool
	// ok reports the list is hashable: at least minHashedInListItems items,
	// a bpchar rule that is the same for every item (below), and items of a
	// single kind, string or int.
	ok      bool
	strs    map[string]struct{}
	ints    map[int64]struct{}
	hasNull bool
	// trimOperand: comparisonOperandsAsBpchar trims the operand's trailing
	// blanks against every item (a bpchar operand).
	trimOperand bool
}

// hashed returns the list's hash set, building it on first use. The bpchar
// normalisation the loop applies per pair (comparisonOperandsAsBpchar) is
// reproduced exactly: with a bpchar operand (opBP > 0) the operand is always
// trimmed and each item is trimmed by its own declared type or bare-literal
// flag; with neither side bpchar-typed nothing is trimmed. A list mixing
// bpchar items under a non-bpchar operand trims the operand per item, which a
// single probe key cannot model, so it is not hashed.
func (c *inListConst) hashed(opBP int64, opLit bool) *inListHash {
	h := &c.hash
	if h.built {
		return h
	}
	h.built = true
	if len(c.values) < minHashedInListItems {
		return h
	}
	if opBP <= 0 {
		for _, bp := range c.itemBP {
			if bp > 0 {
				return h
			}
		}
	}
	h.trimOperand = opBP > 0
	for i, v := range c.values {
		if v.IsNull() {
			h.hasNull = true
			continue
		}
		_, vv := comparisonOperandsAsBpchar(parser.OpEq, NullDatum, v, opBP, c.itemBP[i], opLit, c.itemLit[i])
		switch vv.Kind {
		case KindString:
			if h.strs == nil {
				h.strs = make(map[string]struct{}, len(c.values))
			}
			h.strs[vv.StringValue()] = struct{}{}
		case KindInt:
			if h.ints == nil {
				h.ints = make(map[int64]struct{}, len(c.values))
			}
			h.ints[vv.Int] = struct{}{}
		default:
			return h
		}
	}
	if h.strs != nil && h.ints != nil {
		// compareEq compares an int with a string as text: not a key match.
		h.strs, h.ints = nil, nil
		return h
	}
	h.ok = h.strs != nil || h.ints != nil
	return h
}

// probe answers `operand IN (list)` for a non-NULL operand, reporting
// served=false when the operand's kind is not the set's.
func (h *inListHash) probe(operand Datum, negated bool) (Datum, bool) {
	var found bool
	switch {
	case operand.Kind == KindString && h.strs != nil:
		key := operand.StringValue()
		if h.trimOperand {
			key = strings.TrimRight(key, " ")
		}
		_, found = h.strs[key]
	case operand.Kind == KindInt && h.ints != nil:
		_, found = h.ints[operand.Int]
	default:
		return Datum{}, false
	}
	if found {
		return NewBoolDatum(!negated), true
	}
	if h.hasNull {
		return NullDatum, true
	}
	return NewBoolDatum(negated), true
}
