package xlog

// Publication row filters in the output plugin (pgoutput.c
// pgoutput_row_filter). M0149-0059c.

// RowFilterEval evaluates a relation's row filter over one row: values holds
// each of the relation's columns' text form (nil for NULL), in column order.
type RowFilterEval func(values []*string) (bool, error)

// RowFilterFilter is implemented by a RelationFilter that knows the
// publications' row filters: RowFilter returns the evaluator for rel's
// filter on a change of kind (the OR of the filters of the publications
// that publish kind, pgoutput_row_filter_init's per-pubaction lists), and
// ok = false when no filter applies — some publication publishing kind has
// none, or covers rel as FOR ALL TABLES / TABLES IN SCHEMA.
type RowFilterFilter interface {
	RowFilter(rel *RelationDef, kind ChangeKind) (eval RowFilterEval, ok bool)
}

// rowFilterOutcome is what pgoutput_row_filter decided for one change.
type rowFilterOutcome int

const (
	rowFilterPublish  rowFilterOutcome = iota // publish as the change it is
	rowFilterSkip                             // filtered out
	rowFilterAsInsert                         // UPDATE whose old row did not match, new does
	rowFilterAsDelete                         // UPDATE whose old row matched, new does not
)

// applyRowFilter is pgoutput_row_filter over a change of rel (already the
// publish-as relation). INSERT tests the new row and DELETE the old one; an
// UPDATE without an old row tests the new row, and one with an old row
// tests both — old and new match: UPDATE; only the new: INSERT; only the
// old: DELETE; neither: nothing.
func (p *PgOutput) applyRowFilter(rel *RelationDef, c Change) (rowFilterOutcome, error) {
	rf, ok := p.filter.(RowFilterFilter)
	if !ok {
		return rowFilterPublish, nil
	}
	eval, has := rf.RowFilter(rel, c.Kind)
	if !has {
		return rowFilterPublish, nil
	}
	match := func(raw []byte, old bool) (bool, error) {
		vals, err := p.rowFilterValues(rel, raw, old)
		if err != nil {
			return false, err
		}
		return eval(vals)
	}
	switch c.Kind {
	case ChangeInsert:
		ok, err := match(c.NewTuple, false)
		if err != nil || !ok {
			return rowFilterSkip, err
		}
	case ChangeDelete:
		if len(c.OldTuple) == 0 {
			return rowFilterPublish, nil
		}
		ok, err := match(c.OldTuple, true)
		if err != nil || !ok {
			return rowFilterSkip, err
		}
	case ChangeUpdate:
		newOK, err := match(c.NewTuple, false)
		if err != nil {
			return rowFilterSkip, err
		}
		if len(c.OldTuple) == 0 {
			if !newOK {
				return rowFilterSkip, nil
			}
			return rowFilterPublish, nil
		}
		oldOK, err := match(c.OldTuple, true)
		if err != nil {
			return rowFilterSkip, err
		}
		switch {
		case oldOK && newOK:
			return rowFilterPublish, nil
		case newOK:
			return rowFilterAsInsert, nil
		case oldOK:
			return rowFilterAsDelete, nil
		}
		return rowFilterSkip, nil
	}
	return rowFilterPublish, nil
}

// rowFilterValues decodes a heap tuple of rel into the text values the
// filter sees. An old tuple is the replica identity's (non-identity
// columns NULL) unless the relation is REPLICA IDENTITY FULL — the old
// slot pgoutput_row_filter receives.
func (p *PgOutput) rowFilterValues(rel *RelationDef, raw []byte, old bool) ([]*string, error) {
	keyOnly := false
	if old && rel.ReplIdent != 'f' {
		for _, col := range rel.Columns {
			if col.ReplIdent {
				keyOnly = true
				break
			}
		}
	}
	var body []byte
	var err error
	if keyOnly {
		_, body, err = encodePgoOldTuple(rel, raw, p.snap.RegOut, nil)
	} else {
		body, err = encodePgoTuple(rel.Columns, raw, p.snap.RegOut)
	}
	if err != nil {
		return nil, err
	}
	cols, err := decodeTupleBody(&reader{buf: body})
	if err != nil {
		return nil, err
	}
	vals := make([]*string, len(rel.Columns))
	for i := range vals {
		if i < len(cols) && cols[i].Status == pgoColText {
			s := string(cols[i].Bytes)
			vals[i] = &s
		}
	}
	return vals, nil
}
