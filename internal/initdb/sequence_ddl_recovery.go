package initdb

// Sequence / SERIAL restart persistence — WAL replay side.
//
// goopg's sequence registry (executor seqRegistry) is in-memory only, and a
// SERIAL column's pg_attribute heap row stores the PG-canonical base integer
// atttypid (a real PG18 standby reads those pages), so neither the sequence
// nor the column's serial-ness survives a restart on its own. The write side
// (executor WALLogSequenceState / maybePreLogNextval) emits
// RecordKindSequenceState full-state snapshots — on CREATE TABLE with
// SERIAL/IDENTITY, CREATE/ALTER SEQUENCE, setval, TRUNCATE ... RESTART
// IDENTITY, and every 32nd nextval (upstream SEQ_LOG_VALS pre-logging,
// postgres/src/backend/commands/sequence.c) — plus RecordKindDropSequence on
// removal. This pass walks the WAL once after physical replay AND after
// loadUserTablesFromHeap (the owning tables must already be registered),
// re-registers each surviving sequence with its logged counter, and restores
// the owning column's serial spelling / identity markers, which the INSERT
// auto-increment path (operators_storage.go) keys on.
//
// Replay is last-record-wins: each state record fully re-registers the
// sequence, and a later drop record removes it again.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// replaySequenceDDLRecords reads every WAL record under walDir and applies
// sequence state / drop entries to the executor's sequence registry and the
// catalog's column markers. A missing walDir means "freshly initdb'd
// cluster" and is a no-op. cat may be nil in embedded test setups.
func replaySequenceDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	if _, err := os.Stat(walDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat wal dir: %w", err)
	}

	records, err := wal.ReadAll(walDir, 0)
	if err != nil {
		return fmt.Errorf("read wal: %w", err)
	}

	// live tracks the latest surviving state per sequence key so the
	// catalog-marker fixups below run once per sequence, not per record.
	live := map[string]wal.SequenceStatePayload{}

	for _, rec := range records {
		if len(rec.Payload) == 0 {
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindSequenceState:
			p, derr := wal.DecodeSequenceState(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode sequence-state at lsn %d: %w", rec.StartLSN, derr)
			}
			executor.RestoreSequenceFromWAL(p)
			live[strings.ToLower(p.Name)] = p
		case wal.RecordKindDropSequence:
			name, derr := wal.DecodeDropSequence(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-sequence at lsn %d: %w", rec.StartLSN, derr)
			}
			executor.DropSequence(name)
			delete(live, strings.ToLower(name))
		}
	}

	// Post-pass: for each surviving sequence, restore the owning column's
	// serial spelling / identity markers and re-create the virtual sequence
	// relation (SELECT * FROM seq / pg_class relkind='S' discoverability).
	for _, p := range live {
		seqObjName := parser.ObjectName{Name: p.Name}
		if i := strings.LastIndex(p.Name, "."); i >= 0 {
			seqObjName = parser.ObjectName{Schema: p.Name[:i], Name: p.Name[i+1:]}
		}
		executor.CreateSequenceCatalogRelation(cat, seqObjName, p.Name)

		if p.OwnedBy == "" || (p.ColSpelling == "" && p.IdentityKind == 0) {
			continue
		}
		lastDot := strings.LastIndex(p.OwnedBy, ".")
		if lastDot < 0 {
			continue
		}
		tblName := p.OwnedBy[:lastDot]
		colName := p.OwnedBy[lastDot+1:]
		tblObjName := parser.ObjectName{Name: tblName}
		if i := strings.LastIndex(tblName, "."); i >= 0 {
			tblObjName = parser.ObjectName{Schema: tblName[:i], Name: tblName[i+1:]}
		}
		tbl, ok := cat.LookupTable(tblObjName)
		if !ok || tbl == nil {
			continue // table dropped (its drop-sequence record normally covers this)
		}
		for i := range tbl.Columns {
			if !strings.EqualFold(tbl.Columns[i].Name, colName) {
				continue
			}
			if p.ColSpelling != "" {
				// The heap-reloaded column reads back as the base integer
				// type; restore the serial spelling the auto-increment
				// path keys on.
				tbl.Columns[i].Type.Name = p.ColSpelling
			}
			if p.IdentityKind != 0 {
				tbl.Columns[i].IdentityColumn = true
				tbl.Columns[i].IdentityAlways = p.IdentityKind == 2
			}
			break
		}
	}
	return nil
}
