package initdb

// CREATE OPERATOR FAMILY / CREATE OPERATOR CLASS / DROP OPERATOR CLASS /
// ALTER OPERATOR FAMILY ... ADD|DROP WAL replay (DU-002 restart-persistence
// follow-up to M0119-0004/M0110-0001, closing the loop #65/#66 deferral
// ledger row's "still open" item (1): "CREATE OPERATOR CLASS/CREATE OPERATOR
// FAMILY/pg_amop/pg_amproc member persistence itself — catalog.InMemory's
// userOperatorClasses/userOperatorFamilies/amop/amproc stores are all still
// pure in-memory with zero WAL records, so an opclass/family (and its
// AS-list members) still vanishes on restart even though the
// operators/functions it references now survive").
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores these record
// kinds because goopg has no per-opclass/opfamily file namespace —
// catalog.InMemory's userOperatorFamilies/userOperatorClasses/amOpMembers/
// amProcMembers registries are pure in-memory state with no page-level
// representation (they only back pg_dump round-trip fidelity via the
// pg_opfamily/pg_opclass/pg_amop/pg_amproc virtual views). This recovery
// pass walks the WAL once after physical replay finishes, decodes each
// record, and applies it to the catalog so the post-restart server agrees
// with what the pre-crash server told the client. Mirrors
// replayOperatorDDLRecords (internal/initdb/operator_ddl_recovery.go); runs
// after it (see open.go) since a class's AS-list OPERATOR entries reference
// user operators that must already be restored.
import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// replayOperatorClassDDLRecords reads every WAL record under walDir and
// applies CREATE OPERATOR FAMILY / CREATE OPERATOR CLASS / DROP OPERATOR
// CLASS / ALTER OPERATOR FAMILY ... ADD|DROP entries to cat. A missing
// walDir means "freshly initdb'd cluster" and is treated as a no-op. cat may
// be nil (some embedded test setups), in which case the function returns
// nil without doing any I/O.
func replayOperatorClassDDLRecords(walDir string, cat *catalog.InMemory) error {
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
		case wal.RecordKindCreateOperatorFamily:
			p, derr := wal.DecodeCreateOperatorFamily(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-operator-family at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RegisterUserOperatorFamilyDuringRecovery(&catalog.UserOperatorFamily{
				OID: p.OID, Name: p.Name, Method: p.Method,
			}, p.Schema)
		case wal.RecordKindCreateOperatorClass:
			p, derr := wal.DecodeCreateOperatorClass(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-operator-class at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RegisterUserOperatorClassDuringRecovery(&catalog.UserOperatorClass{
				OID: p.OID, Name: p.Name, Method: p.Method, FamilyOID: p.FamilyOID,
				InTypeOID: p.InTypeOID, IsDefault: p.IsDefault, KeyTypeOID: p.KeyTypeOID,
			}, p.Schema)
		case wal.RecordKindDropOperatorClass:
			oid, derr := wal.DecodeDropOperatorClass(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-operator-class at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.DropUserOperatorClassByOIDDuringRecovery(oid)
		case wal.RecordKindCreateAmOpMember:
			p, derr := wal.DecodeCreateAmOpMember(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-amop-member at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RegisterAmOpMemberDuringRecovery(&catalog.AmOpMember{
				OID: p.OID, FamilyOID: p.FamilyOID, ClassOID: p.ClassOID,
				LeftType: p.LeftType, RightType: p.RightType, Strategy: p.Strategy,
				OperOID: p.OperOID, Method: p.Method, SortFamilyOID: p.SortFamilyOID,
			})
		case wal.RecordKindDropAmOpMember:
			familyOID, leftType, rightType, strategy, derr := wal.DecodeDropAmOpMember(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-amop-member at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RemoveAmOpMember(familyOID, leftType, rightType, strategy)
		case wal.RecordKindCreateAmProcMember:
			p, derr := wal.DecodeCreateAmProcMember(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-amproc-member at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RegisterAmProcMemberDuringRecovery(&catalog.AmProcMember{
				OID: p.OID, FamilyOID: p.FamilyOID, ClassOID: p.ClassOID,
				LeftType: p.LeftType, RightType: p.RightType, ProcNum: p.ProcNum,
				ProcOID: p.ProcOID, Method: p.Method,
			})
		case wal.RecordKindDropAmProcMember:
			familyOID, leftType, rightType, procNum, derr := wal.DecodeDropAmProcMember(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-amproc-member at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RemoveAmProcMember(familyOID, leftType, rightType, procNum)
		case wal.RecordKindDropOperatorFamily:
			oid, derr := wal.DecodeDropOperatorFamily(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-operator-family at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.DropUserOperatorFamilyByOIDDuringRecovery(oid)
		}
	}
	return nil
}
