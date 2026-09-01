package multixact

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// BenchmarkMembers measures resolving a MultiXactId's members (review/260831
// TA-8). Every tuple whose xmax is a multixact resolves through here, so the
// parallel case is the one that matters: the store used to take an exclusive
// mutex for a pure read.
func BenchmarkMembers(b *testing.B) {
	s := NewStore()
	id, err := s.Create(
		Member{Xid: storage.TransactionID(100), Status: StatusForShare},
		Member{Xid: storage.TransactionID(101), Status: StatusForUpdate},
	)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, ok := s.Members(id); !ok {
				b.Fatal("members missing")
			}
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, ok := s.Members(id); !ok {
					b.Fatal("members missing")
				}
			}
		})
	})
}
