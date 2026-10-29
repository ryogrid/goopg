package xlog

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// Logical decoding messages (replication/logical/message.c, message.h).
//
// pg_logical_emit_message writes one RM_LOGICALMSG_ID / XLOG_LOGICAL_MESSAGE
// record whose main data is an xl_logical_message:
//
//	Oid  dbId          (4)  database the message was emitted from
//	bool transactional (1)  + 3 bytes of struct padding
//	Size prefix_size   (8)  strlen(prefix) + 1
//	Size message_size  (8)
//	char message[]          prefix, NUL, then the message bytes
//
// SizeOfLogicalMessage = offsetof(xl_logical_message, message) = 24. Redo is
// a no-op (logicalmsg_redo); the record exists for logical decoding, which
// queues a transactional message in its transaction and hands a
// non-transactional one to the output plugin at once
// (ReorderBufferQueueMessage).

// sizeOfLogicalMessage is SizeOfLogicalMessage (message.h).
const sizeOfLogicalMessage = 24

// LogicalMessage is a decoded xl_logical_message.
type LogicalMessage struct {
	DBOid         uint32
	Transactional bool
	Prefix        string
	Content       []byte
}

// EncodeLogicalMessagePG builds the RM_LOGICALMSG record LogLogicalMessage
// inserts. xid is the record header's xl_xid: the emitting transaction's xid
// (assigned first for a transactional message, as LogLogicalMessage's
// GetCurrentTransactionId does), or whatever it already has for a
// non-transactional one (GetCurrentTransactionIdIfAny), possibly zero.
func EncodeLogicalMessagePG(xid storage.TransactionID, m LogicalMessage) ([]byte, error) {
	mainData := make([]byte, sizeOfLogicalMessage, sizeOfLogicalMessage+len(m.Prefix)+1+len(m.Content))
	binary.LittleEndian.PutUint32(mainData[0:4], m.DBOid)
	if m.Transactional {
		mainData[4] = 1
	}
	binary.LittleEndian.PutUint64(mainData[8:16], uint64(len(m.Prefix)+1))
	binary.LittleEndian.PutUint64(mainData[16:24], uint64(len(m.Content)))
	mainData = append(mainData, m.Prefix...)
	mainData = append(mainData, 0)
	mainData = append(mainData, m.Content...)
	return framedAssemble(RmgrLogicalMessage, xlogLogicalMessage, uint32(xid), mainData, nil)
}

// DecodeLogicalMessage parses an XLOG_LOGICAL_MESSAGE record's main data.
func DecodeLogicalMessage(mainData []byte) (LogicalMessage, error) {
	if len(mainData) < sizeOfLogicalMessage {
		return LogicalMessage{}, errors.New("wal: logical message: truncated header")
	}
	prefixSize := binary.LittleEndian.Uint64(mainData[8:16])
	messageSize := binary.LittleEndian.Uint64(mainData[16:24])
	rest := mainData[sizeOfLogicalMessage:]
	if prefixSize == 0 || prefixSize > uint64(len(rest)) || messageSize > uint64(len(rest))-prefixSize {
		return LogicalMessage{}, fmt.Errorf("wal: logical message: sizes %d+%d exceed %d payload bytes", prefixSize, messageSize, len(rest))
	}
	if rest[prefixSize-1] != 0 {
		return LogicalMessage{}, errors.New("wal: logical message: prefix is not NUL-terminated")
	}
	return LogicalMessage{
		DBOid:         binary.LittleEndian.Uint32(mainData[0:4]),
		Transactional: mainData[4] != 0,
		Prefix:        string(rest[:prefixSize-1]),
		Content:       append([]byte(nil), rest[prefixSize:prefixSize+messageSize]...),
	}, nil
}

// MessagePlugin is the optional output-plugin callback for logical decoding
// messages — OutputPluginCallbacks.message_cb, which PG also leaves
// optional. A plugin without it never sees messages.
type MessagePlugin interface {
	// LogicalMessage delivers one message. A transactional message arrives
	// between its transaction's Begin and Commit, in WAL order with the row
	// changes; a non-transactional one arrives as soon as it is decoded,
	// outside any transaction frame. lsn is the end of the message record
	// (the decoding buffer's endptr).
	LogicalMessage(xid storage.TransactionID, lsn uint64, transactional bool, prefix string, content []byte) error
}

// applyLogicalMessage is logicalmsg_decode + ReorderBufferQueueMessage: a
// message from another database is skipped; a transactional one is queued
// in its transaction (so an abort drops it and a commit delivers it in
// order); a non-transactional one goes to the plugin now.
func (d *Decoder) applyLogicalMessage(xid storage.TransactionID, lsn uint64, m LogicalMessage) error {
	if d.dbOid != 0 && m.DBOid != d.dbOid {
		return nil
	}
	if m.Transactional {
		if xid == storage.InvalidTransactionID {
			return errors.New("wal: transactional logical message without a transaction id")
		}
		d.buf.Append(xid, Change{Kind: ChangeMessage, LSN: lsn, MsgPrefix: m.Prefix, MsgContent: m.Content})
		return nil
	}
	mp, ok := d.plugin.(MessagePlugin)
	if !ok {
		return nil
	}
	return mp.LogicalMessage(xid, lsn, false, m.Prefix, m.Content)
}

// SetDatabase limits decoded logical messages to those emitted from
// database dbOid, as logicalmsg_decode compares dbId with the slot's
// database. Zero disables the filter.
func (d *Decoder) SetDatabase(dbOid uint32) { d.dbOid = dbOid }
