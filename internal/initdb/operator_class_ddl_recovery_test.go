package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestOperatorClassDDLRecoveryReplaysCreate confirms the DU-002
// restart-persistence hook (M0119-0004/M0110-0001, closing the loop #65/#66
// deferral ledger row's "still open" item (1)): CREATE OPERATOR FAMILY /
// CREATE OPERATOR CLASS / pg_amop / pg_amproc WAL records written by a
// pre-crash run are replayed into the catalog on the post-crash Open path,
// so pg_opfamily/pg_opclass/pg_amop/pg_amproc (pg_dump's dumpOpfamily/
// dumpOpclass) round-trip after restart even though the goopg process never
// wrote a JSON snapshot. Every field, including the AS-list members, survives
// the restart with stable OIDs.
func TestOperatorClassDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	famPayload := wal.CreateOperatorFamilyPayload{OID: 40920, Schema: "public", Name: "myintops", Method: 403}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateOperatorFamily(famPayload)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-operator-family: %v", werr)
	}
	classPayload := wal.CreateOperatorClassPayload{
		OID: 40921, Schema: "public", Name: "myintops", Method: 403, FamilyOID: famPayload.OID,
		InTypeOID: 23, KeyTypeOID: 0, IsDefault: true,
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateOperatorClass(classPayload)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-operator-class: %v", werr)
	}
	amopPayload := wal.AmOpMemberPayload{
		OID: 40922, FamilyOID: famPayload.OID, ClassOID: classPayload.OID,
		LeftType: 23, RightType: 23, Strategy: 3, OperOID: 96, Method: 403,
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAmOpMember(amopPayload)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-amop-member: %v", werr)
	}
	amprocPayload := wal.AmProcMemberPayload{
		OID: 40923, FamilyOID: famPayload.OID, ClassOID: classPayload.OID,
		LeftType: 23, RightType: 23, ProcNum: 1, ProcOID: 351, Method: 403,
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAmProcMember(amprocPayload)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-amproc-member: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the family/class/members without
	// any JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()
	im := operatorCatalog(t, rt2)

	fam, ok := im.LookupUserOperatorFamily("public", "myintops", 403)
	if !ok {
		t.Fatalf("after WAL replay, operator family \"myintops\" not found")
	}
	if fam.OID != famPayload.OID {
		t.Errorf("family OID = %d, want %d", fam.OID, famPayload.OID)
	}

	oc, ok := im.LookupUserOperatorClass("public", "myintops", 403)
	if !ok {
		t.Fatalf("after WAL replay, operator class \"myintops\" not found")
	}
	if oc.OID != classPayload.OID || oc.FamilyOID != famPayload.OID || oc.InTypeOID != 23 || !oc.IsDefault {
		t.Errorf("after WAL replay, class = %+v, want matching payload %+v", oc, classPayload)
	}
	if !im.HasOpClass("myintops") {
		t.Error("after WAL replay, legacy opClassSchemas registry does not have \"myintops\" (DROP OPERATOR CLASS would 42704)")
	}

	var foundOp, foundProc bool
	for _, m := range im.ListAmOpMembers() {
		if m.OID == amopPayload.OID {
			foundOp = true
			if m.FamilyOID != famPayload.OID || m.ClassOID != classPayload.OID || m.Strategy != 3 || m.OperOID != 96 {
				t.Errorf("after WAL replay, amop member = %+v, want matching payload %+v", m, amopPayload)
			}
		}
	}
	if !foundOp {
		t.Error("after WAL replay, amop member not found")
	}
	for _, m := range im.ListAmProcMembers() {
		if m.OID == amprocPayload.OID {
			foundProc = true
			if m.FamilyOID != famPayload.OID || m.ClassOID != classPayload.OID || m.ProcNum != 1 || m.ProcOID != 351 {
				t.Errorf("after WAL replay, amproc member = %+v, want matching payload %+v", m, amprocPayload)
			}
		}
	}
	if !foundProc {
		t.Error("after WAL replay, amproc member not found")
	}
}

// TestOperatorClassDDLRecoveryReplaysDropAfterCreate confirms a CREATE
// OPERATOR CLASS followed by a DROP OPERATOR CLASS cancels out — the class
// row and every amop/amproc row it owns are gone, matching the live
// DropUserOperatorClass cleanup.
func TestOperatorClassDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateOperatorFamily(wal.CreateOperatorFamilyPayload{
		OID: 40930, Schema: "public", Name: "dropme", Method: 403,
	})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-operator-family: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateOperatorClass(wal.CreateOperatorClassPayload{
		OID: 40931, Schema: "public", Name: "dropme", Method: 403, FamilyOID: 40930, InTypeOID: 23, IsDefault: true,
	})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-operator-class: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAmOpMember(wal.AmOpMemberPayload{
		OID: 40932, FamilyOID: 40930, ClassOID: 40931, LeftType: 23, RightType: 23, Strategy: 3, OperOID: 96, Method: 403,
	})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-amop-member: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropOperatorClass(40931)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop-operator-class: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()
	im := operatorCatalog(t, rt2)

	if oc, ok := im.LookupUserOperatorClass("public", "dropme", 403); ok {
		t.Errorf("after CREATE + DROP replay, class \"dropme\" = %+v, want not found", oc)
	}
	if im.HasOpClass("dropme") {
		t.Error("after CREATE + DROP replay, legacy opClassSchemas registry still has \"dropme\"")
	}
	for _, m := range im.ListAmOpMembers() {
		if m.OID == 40932 {
			t.Errorf("after CREATE + DROP replay, amop member %+v still present", m)
		}
	}
	// The owning family itself is untouched by DROP OPERATOR CLASS (mirrors
	// the live DropUserOperatorClass behavior — only the class + its
	// amop/amproc rows are removed).
	if _, ok := im.LookupUserOperatorFamily("public", "dropme", 403); !ok {
		t.Error("after CREATE + DROP (class only) replay, family \"dropme\" unexpectedly removed too")
	}
}

// TestOperatorClassDDLRecoveryReplaysDropOperatorFamilyAfterCreate confirms a
// CREATE OPERATOR FAMILY followed by a DROP OPERATOR FAMILY cancels out
// across a restart — the family row and every loose amop/amproc row it owns
// are gone, matching the live DropUserOperatorFamily cleanup. Closes the loop
// #69 ledger row's "DROP OPERATOR FAMILY never actually calls
// DropUserOperatorFamily" discovery.
func TestOperatorClassDDLRecoveryReplaysDropOperatorFamilyAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateOperatorFamily(wal.CreateOperatorFamilyPayload{
		OID: 40950, Schema: "public", Name: "dropfam", Method: 403,
	})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-operator-family: %v", werr)
	}
	// A loose member (ClassOID=0) owned by the family — mirrors ALTER
	// OPERATOR FAMILY ... ADD — must be purged along with the family itself.
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAmOpMember(wal.AmOpMemberPayload{
		OID: 40951, FamilyOID: 40950, ClassOID: 0, LeftType: 23, RightType: 23, Strategy: 1, OperOID: 97, Method: 403,
	})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-amop-member: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropOperatorFamily(40950)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop-operator-family: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()
	im := operatorCatalog(t, rt2)

	if fam, ok := im.LookupUserOperatorFamily("public", "dropfam", 403); ok {
		t.Errorf("after CREATE + DROP OPERATOR FAMILY replay, family \"dropfam\" = %+v, want not found", fam)
	}
	for _, m := range im.ListAmOpMembers() {
		if m.OID == 40951 {
			t.Errorf("after CREATE + DROP OPERATOR FAMILY replay, loose amop member %+v still present", m)
		}
	}
}

// TestOperatorClassDDLRecoveryReplaysAlterAddThenDropMember confirms an
// ALTER OPERATOR FAMILY ... ADD member followed by its own ... DROP cancels
// out, exercising RecordKindDropAmOpMember/RecordKindDropAmProcMember.
func TestOperatorClassDDLRecoveryReplaysAlterAddThenDropMember(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateOperatorFamily(wal.CreateOperatorFamilyPayload{
		OID: 40940, Schema: "public", Name: "loosefam", Method: 403,
	})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-operator-family: %v", werr)
	}
	// A "loose" member (no owning class, ClassOID=0) — mirrors ALTER
	// OPERATOR FAMILY ... ADD.
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAmOpMember(wal.AmOpMemberPayload{
		OID: 40941, FamilyOID: 40940, ClassOID: 0, LeftType: 23, RightType: 23, Strategy: 1, OperOID: 97, Method: 403,
	})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-amop-member: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAmProcMember(wal.AmProcMemberPayload{
		OID: 40942, FamilyOID: 40940, ClassOID: 0, LeftType: 23, RightType: 23, ProcNum: 1, ProcOID: 351, Method: 403,
	})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-amproc-member: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropAmOpMember(40940, 23, 23, 1)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop-amop-member: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()
	im := operatorCatalog(t, rt2)

	for _, m := range im.ListAmOpMembers() {
		if m.OID == 40941 {
			t.Errorf("after ADD + DROP replay, amop member %+v still present", m)
		}
	}
	var foundProc bool
	for _, m := range im.ListAmProcMembers() {
		if m.OID == 40942 {
			foundProc = true
		}
	}
	if !foundProc {
		t.Error("after ADD (function, never dropped) replay, amproc member not found")
	}
}

// TestReplayOperatorClassDDLRecordsHandlesMissingWalDir verifies the
// recovery hook is idempotent when invoked against a missing pg_wal
// directory (brand new initdb).
func TestReplayOperatorClassDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayOperatorClassDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(cat.ListUserOperatorClasses()) != 0 {
		t.Errorf("no-op replay should not register anything, got %+v", cat.ListUserOperatorClasses())
	}
}

// TestReplayOperatorClassDDLRecordsHandlesNilCatalog verifies the recovery
// hook tolerates a nil catalog (mirrors the nil-registry guard the other DDL
// recovery drivers have for embedded test setups).
func TestReplayOperatorClassDDLRecordsHandlesNilCatalog(t *testing.T) {
	if err := replayOperatorClassDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), nil); err != nil {
		t.Fatalf("replay with nil catalog: %v", err)
	}
}
