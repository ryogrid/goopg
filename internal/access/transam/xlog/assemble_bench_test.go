package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// benchAssembleInputs builds one record's inputs. image is "" (no image),
// "hole" (a standard page, so the free-space hole is omitted) or "full" (a page
// with no usable standard header, so all 8192 bytes are emitted).
func benchAssembleInputs(image string) ([]byte, []BlockRef) {
	mainData := make([]byte, 24)
	for i := range mainData {
		mainData[i] = byte(i)
	}
	data := make([]byte, 96)
	for i := range data {
		data[i] = byte(i)
	}
	ref := BlockRef{ID: 0, Rel: storage.RelFileNode{DBOid: 1, RelOid: 16384}, Block: 42, Data: data}
	switch image {
	case "hole":
		page := make(storage.Page, storage.BlockSize)
		if err := storage.InitPage(page); err != nil {
			panic(err)
		}
		ref.Image = &FullPageImage{Page: page}
	case "full":
		page := make(storage.Page, storage.BlockSize)
		for i := range page {
			page[i] = 0xFF // no usable standard header: the whole page ships
		}
		ref.Image = &FullPageImage{Page: page}
	}
	return mainData, []BlockRef{ref}
}

// BenchmarkAssembleXLogRecord measures WAL record assembly (review/260831
// XL-68): the header and payload regions used to grow from nil and then be
// concatenated into a third buffer, and a full-page image was built in its own
// page-sized buffer before being copied into the payload.
func BenchmarkAssembleXLogRecord(b *testing.B) {
	for _, image := range []string{"", "hole", "full"} {
		name := "data-only"
		if image != "" {
			name = "fpi-" + image
		}
		mainData, blocks := benchAssembleInputs(image)
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				out, err := assembleXLogRecord(mainData, blocks)
				if err != nil || len(out) == 0 {
					b.Fatalf("assembleXLogRecord: %v", err)
				}
			}
		})
	}
}

// BenchmarkEncodeRecordXLog measures the goopg-record encode path
// (review/260831 XL-14): the main-data chunk used to be wrapped in its own
// buffer and then copied into the output record, so every WAL record copied its
// payload twice.
func BenchmarkEncodeRecordXLog(b *testing.B) {
	for _, size := range []int{64, 4096} {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i)
		}
		b.Run(map[bool]string{true: "payload=64", false: "payload=4096"}[size == 64], func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				out, n, err := encodeRecordXLog(payload, 0)
				if err != nil || n == 0 || len(out) == 0 {
					b.Fatalf("encodeRecordXLog: %v", err)
				}
			}
		})
	}
}
