package mvcc

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func TestTupleVisibleBasicCases(t *testing.T) {
	snap := Snapshot{
		Xmin:       storage.TransactionID(10),
		Xmax:       storage.TransactionID(20),
		InProgress: []storage.TransactionID{12, 15},
	}
	current := storage.TransactionID(18)

	tests := []struct {
		name string
		h    storage.HeapTupleHeader
		want bool
	}{
		{
			name: "committed insert not deleted",
			h: storage.HeapTupleHeader{
				Xmin: 8,
				Xmax: storage.InvalidTransactionID,
			},
			want: true,
		},
		{
			name: "insert in progress invisible",
			h: storage.HeapTupleHeader{
				Xmin: 12,
				Xmax: storage.InvalidTransactionID,
			},
			want: false,
		},
		{
			name: "future insert invisible",
			h: storage.HeapTupleHeader{
				Xmin: 30,
				Xmax: storage.InvalidTransactionID,
			},
			want: false,
		},
		{
			name: "committed delete invisible",
			h: storage.HeapTupleHeader{
				Xmin: 8,
				Xmax: 9,
			},
			want: false,
		},
		{
			name: "delete in progress still visible",
			h: storage.HeapTupleHeader{
				Xmin: 8,
				Xmax: 12,
			},
			want: true,
		},
		{
			name: "future delete still visible",
			h: storage.HeapTupleHeader{
				Xmin: 8,
				Xmax: 22,
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TupleVisible(tc.h, snap, current); got != tc.want {
				t.Fatalf("TupleVisible=%v want=%v header=%+v", got, tc.want, tc.h)
			}
		})
	}
}

func TestTupleVisibleOwnXIDRules(t *testing.T) {
	current := storage.TransactionID(42)
	snap := Snapshot{
		Xmin:       40,
		Xmax:       50,
		InProgress: []storage.TransactionID{42},
	}

	visibleOwnInsert := storage.HeapTupleHeader{
		Xmin: current,
		Xmax: storage.InvalidTransactionID,
	}
	if !TupleVisible(visibleOwnInsert, snap, current) {
		t.Fatal("own insert should be visible")
	}

	deletedOwnInsert := storage.HeapTupleHeader{
		Xmin: current,
		Xmax: current,
	}
	if TupleVisible(deletedOwnInsert, snap, current) {
		t.Fatal("tuple deleted by current transaction should be invisible")
	}
}
