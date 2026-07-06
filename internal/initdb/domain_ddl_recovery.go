package initdb

// CREATE/DROP DOMAIN WAL replay (M0122-0005 restart-persistence follow-up,
// deferral ledger 2026-07-06 row: "domains have no restart persistence at
// all — CREATE DOMAIN has no WAL record and no heap-reload path in
// internal/initdb").
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the domain record
// kinds (119-120) because goopg has no per-domain file namespace —
// catalog.InMemory's domains map is a pure in-memory registry with no
// page-level state to reconstruct (the registry backs both pg_dump
// round-trip fidelity and runtime domain-typed column validation). This
// recovery pass walks the WAL once after physical replay finishes, decodes
// each domain DDL record, and applies it to the catalog so the post-restart
// server agrees with what the pre-crash server told the client. Mirrors
// replayRangeTypeDDLRecords (internal/initdb/range_type_ddl_recovery.go);
// domains, like range types, are not schema-scoped (keyed by name only), so
// this does not depend on schema replay having run first.
//
// Only CREATE DOMAIN's own definition (base type, NOT NULL, DEFAULT, CHECK
// constraints, owner) and DROP DOMAIN are captured here. The later
// `ALTER DOMAIN RENAME TO`/`OWNER TO`/`RENAME CONSTRAINT`/`ADD`/`DROP
// CONSTRAINT`/`SET`/`DROP DEFAULT` follow-ups landed without their own WAL
// records (recorded as a still-open gap in the matching ledger row) — none of
// those mutations survive a restart yet, only the state as of CREATE DOMAIN
// plus whatever was folded into it.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// replayDomainDDLRecords reads every WAL record under walDir and applies
// CREATE/DROP DOMAIN entries to cat. A missing walDir means "freshly
// initdb'd cluster" and is treated as a no-op. cat may be nil (some embedded
// test setups), in which case the function returns nil without doing any
// I/O.
func replayDomainDDLRecords(walDir string, cat *catalog.InMemory) error {
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

	for _, rec := range records {
		if len(rec.Payload) == 0 || !wal.IsGoopgNativeRecord(rec) {
			// See replayRangeTypeDDLRecords: skip PG-canonical records whose
			// MainData could coincidentally collide with a small RecordKind
			// byte value (M0106-0011).
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindCreateDomain:
			p, derr := wal.DecodeCreateDomain(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-domain at lsn %d: %w", rec.StartLSN, derr)
			}
			d := &catalog.Domain{
				Name:       p.Name,
				OID:        p.OID,
				ArrayOID:   p.ArrayOID,
				Base:       catalog.Type{Name: p.BaseName, Args: p.BaseArgs},
				BaseOID:    p.BaseOID,
				BaseIsEnum: p.BaseIsEnum,
				NotNull:    p.NotNull,
				Owner:      p.Owner,
			}
			if p.DefaultSQL != "" {
				if expr, perr := parser.ParseExpr(p.DefaultSQL); perr == nil {
					d.Default = expr
				}
				// A deparse/reparse gap for an exotic expression degrades to
				// no DEFAULT rather than failing startup, mirroring
				// replayColumnDefaultsRecords.
			}
			for _, c := range p.Checks {
				d.Checks = append(d.Checks, catalog.DomainCheck{
					Name:     c.Name,
					Expr:     c.Expr,
					OID:      c.OID,
					InValues: c.InValues,
				})
			}
			cat.RegisterDomainDuringRecovery(d)
		case wal.RecordKindDropDomain:
			name, derr := wal.DecodeDropDomain(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-domain at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.DropDomainDuringRecovery(name)
		}
	}
	return nil
}
