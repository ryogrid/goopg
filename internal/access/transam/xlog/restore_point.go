package xlog

import (
	"bytes"
	"encoding/binary"
)

// maxRestorePointName is MAXFNAMELEN - 1: the longest restore point name
// pg_create_restore_point accepts.
const maxRestorePointName = 63

// sizeOfXlRestorePoint is sizeof(xl_restore_point): TimestampTz rp_time and
// char rp_name[MAXFNAMELEN].
const sizeOfXlRestorePoint = 8 + 64

// MaxRestorePointName is the longest restore point name, in bytes.
const MaxRestorePointName = maxRestorePointName

// EncodeRestorePointPG builds the RM_XLOG XLOG_RESTORE_POINT record
// XLogRestorePoint writes: the creation time (TimestampTz, microseconds since
// 2000-01-01) and the NUL-padded name. M0149-0023a.
func EncodeRestorePointPG(name string, rpTime int64) ([]byte, error) {
	main := make([]byte, sizeOfXlRestorePoint)
	binary.LittleEndian.PutUint64(main, uint64(rpTime))
	copy(main[8:8+maxRestorePointName], name)
	return framedAssemble(RmgrXLog, xlogXLogRestorePoint, 0, main, nil)
}

// RestorePointOf returns the restore point a replayed record carries: ok is
// false unless r is an XLOG_RESTORE_POINT.
func RestorePointOf(r Record) (name string, rpTime int64, ok bool) {
	if r.XLog == nil || r.XLog.Header.Rmid != RmgrXLog || r.XLog.Header.Info&XLRRmgrInfoMask != xlogXLogRestorePoint {
		return "", 0, false
	}
	main := r.XLog.MainData
	if len(main) < sizeOfXlRestorePoint {
		return "", 0, false
	}
	n := main[8:sizeOfXlRestorePoint]
	if i := bytes.IndexByte(n, 0); i >= 0 {
		n = n[:i]
	}
	return string(n), int64(binary.LittleEndian.Uint64(main)), true
}
