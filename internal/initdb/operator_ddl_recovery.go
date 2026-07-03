package initdb

// CREATE/DROP OPERATOR WAL replay (DU-002 restart-persistence follow-up to
// M0119-0004/M0110-0001, discovered while verifying the loop #64 CREATE
// TYPE ... AS RANGE opclass/collation follow-up — see the deferral ledger).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the operator
// record kinds (83-84) because goopg has no per-operator file namespace —
// catalog.InMemory's userOperators map is a pure in-memory registry with no
// page-level state to reconstruct (the registry only backs pg_dump
// round-trip fidelity via the pg_operator virtual view). This recovery pass
// walks the WAL once after physical replay finishes, decodes each operator
// DDL record, and applies it to the catalog so the post-restart server
// agrees with what the pre-crash server told the client. Mirrors
// replayRangeTypeDDLRecords (internal/initdb/range_type_ddl_recovery.go);
// unlike range types, an operator IS schema-scoped (its registry key
// embeds schema), so this depends on replaySchemaDDLRecords having already
// restored the schema registry — RegisterUserOperatorDuringRecovery
// resolves the schema name to its (possibly reassigned) post-restart OID,
// the same pattern RegisterUserAggregateDuringRecovery uses.
import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// replayOperatorDDLRecords reads every WAL record under walDir and applies
// CREATE/DROP OPERATOR entries to cat. A missing walDir means "freshly
// initdb'd cluster" and is treated as a no-op. cat may be nil (some
// embedded test setups), in which case the function returns nil without
// doing any I/O.
func replayOperatorDDLRecords(walDir string, cat *catalog.InMemory) error {
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
			// wal.IsGoopgNativeRecord rules out a PG-native/canonical
			// XLogRecord whose MainData is raw PG struct bytes with no
			// goopg kind-byte tag at Payload[0] — scanning it against the
			// small custom RecordKind constants below risks a coincidental
			// false-positive byte match (M0106-0011), so skip these
			// records entirely.
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindCreateOperator:
			p, derr := wal.DecodeCreateOperator(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-operator at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RegisterUserOperatorDuringRecovery(&catalog.UserOperator{
				OID:           p.OID,
				Name:          p.Name,
				LeftType:      p.LeftType,
				RightType:     p.RightType,
				FuncOID:       p.FuncOID,
				Owner:         p.Owner,
				CommutatorOID: p.CommutatorOID,
				NegatorOID:    p.NegatorOID,
				RestrictOID:   p.RestrictOID,
				JoinOID:       p.JoinOID,
				CanMerge:      p.CanMerge,
				CanHash:       p.CanHash,
			}, p.Schema)
		case wal.RecordKindDropOperator:
			oid, derr := wal.DecodeDropOperator(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-operator at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.DropUserOperatorByOIDDuringRecovery(oid)
		}
	}
	return nil
}
