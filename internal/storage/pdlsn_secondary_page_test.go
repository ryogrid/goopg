package storage

import "testing"

// TestMarkDirtyCoveredByRecordStampsSecondaryPage pins the M0131-S26 contract
// for the SECONDARY page of a multi-page change record (the cross-page heap
// UPDATE's old page): the page must end up carrying the record's LSN, because
//
//   - flushSlots computes flushTo from max(pd_lsn) over the batch, so a stale
//     pd_lsn lets the mutation reach disk ahead of its WAL record, and
//   - a replaying PG skips a record only when pd_lsn is already at or past it
//     (`lsn <= PageGetLSN(page)`), so a stale pd_lsn misdescribes which
//     records the page already contains.
//
// The test also pins the two things the method must NOT do: emit a second
// image for a page already imaged this epoch, and advance nativeImageLSN (no
// image of THIS page exists at the record's LSN — advancing it would suppress
// the page's next first-touch FPI).
func TestMarkDirtyCoveredByRecordStampsSecondaryPage(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	var nextLSN LSN = 100
	var images int
	logFPI := func(_ RelFileNode, _ BlockNumber, _ Page) (LSN, error) {
		images++
		nextLSN++
		return nextLSN, nil
	}
	pool, err := NewPool(mgr, PoolConfig{
		Slots:          4,
		LogPageImage:   logFPI,
		FullPageWrites: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 626, Fork: MainFork}
	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(s)
	if err := InitPage(s.Page()); err != nil {
		t.Fatal(err)
	}

	// First touch of the epoch: MarkDirty emits the image and stamps pd_lsn
	// with the image's LSN. After this the page is "already imaged", which is
	// precisely the state in which the old code left the secondary page's
	// pd_lsn untouched.
	s.Lock()
	s.Page()[300]++
	pool.MarkDirty(s)
	s.Unlock()
	if images != 1 {
		t.Fatalf("first-touch images = %d, want 1", images)
	}
	imageLSN := MustHeader(s.Page()).LSN()
	if imageLSN == 0 {
		t.Fatal("first-touch MarkDirty left pd_lsn at 0")
	}
	imageWatermark := s.nativeImageLSN.Load()

	// Now the secondary-page case: another page's MarkDirtyLogicalChange
	// appended a record that also describes THIS page's mutation.
	nextLSN += 10
	recLSN := nextLSN
	s.Lock()
	s.Page()[301]++
	pool.MarkDirtyCoveredByRecordLocked(s, recLSN)
	s.Unlock()

	if got := MustHeader(s.Page()).LSN(); got != recLSN {
		t.Fatalf("pd_lsn = %d after the covering record at %d (image was %d); "+
			"a stale pd_lsn breaks WAL-before-data and makes a replaying PG re-apply", got, recLSN, imageLSN)
	}
	if images != 1 {
		t.Fatalf("images = %d; the page was already imaged this epoch, no second image is due", images)
	}
	if got := s.nativeImageLSN.Load(); got != imageWatermark {
		t.Fatalf("nativeImageLSN = %d, want %d unchanged: no IMAGE of this page exists at the record's LSN, "+
			"so advancing the watermark would suppress the next first-touch FPI", got, imageWatermark)
	}

	// pd_lsn is raised, never lowered: a covering record older than the page's
	// current LSN (e.g. maybeEmitFPI just stamped a larger image LSN) must not
	// rewind it.
	s.Lock()
	s.Page()[302]++
	pool.MarkDirtyCoveredByRecordLocked(s, recLSN-5)
	s.Unlock()
	if got := MustHeader(s.Page()).LSN(); got != recLSN {
		t.Fatalf("pd_lsn = %d after a LOWER covering LSN; want it held at %d", got, recLSN)
	}
}

// TestMarkDirtyCoveredByRecordEmitsFirstTouchImage pins the other half: on the
// first touch of an epoch the secondary page still owes an FPI exactly as
// plain MarkDirty would emit one, and the resulting pd_lsn is the larger of
// the image LSN and the covering record's LSN.
func TestMarkDirtyCoveredByRecordEmitsFirstTouchImage(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	var nextLSN LSN = 500
	var images int
	logFPI := func(_ RelFileNode, _ BlockNumber, _ Page) (LSN, error) {
		images++
		nextLSN++
		return nextLSN, nil
	}
	pool, err := NewPool(mgr, PoolConfig{
		Slots:          4,
		LogPageImage:   logFPI,
		FullPageWrites: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 627, Fork: MainFork}
	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(s)
	if err := InitPage(s.Page()); err != nil {
		t.Fatal(err)
	}

	// The covering record was appended BEFORE the image (the emitter runs
	// first in MarkDirtyLogicalChange), so the image LSN is the larger one.
	recLSN := nextLSN
	s.Lock()
	s.Page()[300]++
	pool.MarkDirtyCoveredByRecordLocked(s, recLSN)
	s.Unlock()

	if images != 1 {
		t.Fatalf("images = %d, want 1 (first touch of the epoch still owes an FPI)", images)
	}
	if got, want := MustHeader(s.Page()).LSN(), nextLSN; got != want {
		t.Fatalf("pd_lsn = %d, want the image LSN %d (max of image and record LSN)", got, want)
	}
	if got, want := s.nativeImageLSN.Load(), uint64(nextLSN); got != want {
		t.Fatalf("nativeImageLSN = %d, want %d (maybeEmitFPI's own image watermark)", got, want)
	}
}
