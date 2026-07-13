package storage

import (
	"testing"
	"time"
)

// TestFPIRedoPublicationClosesWindow pins the perf-optimize3-dash/03 fix:
// the FPI decision is the per-record pd_lsn <= publishedRedo test against a
// pointer published at checkpoint START. The adversarial scenario the old
// design failed (option (a), rejected): a page already imaged in the prior
// epoch is modified after the new redo is published — under the old
// fpiSinceCheckpoint bool the write emitted NO image (bool still true from
// the prior epoch) while its LSN was > redo, leaving replay-from-redo with
// an image-less record onto a potentially torn page. Under option (b) the
// pd_lsn (stamped by the PRIOR epoch's image, <= the NEW redo) forces a
// fresh image — there is no sweep to race.
func TestFPIRedoPublicationClosesWindow(t *testing.T) {
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
		Slots:          2,
		LogPageImage:   logFPI,
		FullPageWrites: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 601, Fork: MainFork}
	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(s)

	touch := func() {
		s.Lock()
		s.Page()[300]++
		pool.MarkDirty(s)
		s.Unlock()
	}
	emitViaChangeRecord := func() {
		s.Lock()
		s.Page()[301]++
		if err := pool.MarkDirtyChangeRecord(s, func() (LSN, error) {
			nextLSN++
			return nextLSN, nil
		}); err != nil {
			t.Fatal(err)
		}
		s.Unlock()
	}
	emitViaLogicalChange := func() {
		s.Lock()
		s.Page()[302]++
		if err := pool.MarkDirtyLogicalChange(s, func() (LSN, error) {
			nextLSN++
			return nextLSN, nil
		}); err != nil {
			t.Fatal(err)
		}
		s.Unlock()
	}

	// Epoch 1 (redo=0): first touch images, subsequent touches do not.
	touch()
	if images != 1 {
		t.Fatalf("epoch1 first touch: images=%d, want 1", images)
	}
	touch()
	emitViaChangeRecord()
	if images != 1 {
		t.Fatalf("epoch1 later touches: images=%d, want still 1", images)
	}

	// Checkpoint start: publish a redo ABOVE the page's pd_lsn — the exact
	// moment the old sweep-based design raced. Every MarkDirty* variant must
	// now re-image on its first post-publication modification.
	pool.PublishRedoRecPtr(uint64(nextLSN) + 10)
	nextLSN += 20

	emitViaLogicalChange() // logical record + fresh image (window closed)
	if images != 2 {
		t.Fatalf("post-publication MarkDirtyLogicalChange: images=%d, want 2", images)
	}
	// pd_lsn is now above the published redo again — no further images.
	touch()
	emitViaChangeRecord()
	if images != 2 {
		t.Fatalf("post-image touches: images=%d, want still 2", images)
	}

	// A second publication re-arms MarkDirtyChangeRecord's image branch too.
	pool.PublishRedoRecPtr(uint64(nextLSN) + 10)
	nextLSN += 20
	emitViaChangeRecord()
	if images != 3 {
		t.Fatalf("second publication + MarkDirtyChangeRecord: images=%d, want 3", images)
	}
}

// TestPublishRedoBarrierWaitsForInFlightDecision pins the F1 fix: a redo
// publication must not land between a writer's FPI decision and its record
// append. The writer blocks inside its emitter (RLock held); the barrier
// publication must not complete until the emitter finishes.
func TestPublishRedoBarrierWaitsForInFlightDecision(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	pool, err := NewPool(mgr, PoolConfig{Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 602, Fork: MainFork}
	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(s)

	emitterEntered := make(chan struct{})
	releaseEmitter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		s.Lock()
		defer s.Unlock()
		s.Page()[300] = 1
		writerDone <- pool.MarkDirtyChangeRecord(s, func() (LSN, error) {
			close(emitterEntered)
			<-releaseEmitter // hold the RLock critical section open
			return LSN(500), nil
		})
	}()
	<-emitterEntered

	published := make(chan uint64, 1)
	go func() {
		published <- pool.PublishRedoBarrier(func() uint64 { return 1000 })
	}()

	select {
	case r := <-published:
		t.Fatalf("PublishRedoBarrier completed (redo=%d) while a decision->append section was in flight", r)
	case <-time.After(100 * time.Millisecond):
		// expected: barrier is waiting on the writer's RLock
	}
	close(releaseEmitter)
	if err := <-writerDone; err != nil {
		t.Fatalf("MarkDirtyChangeRecord: %v", err)
	}
	select {
	case r := <-published:
		if r != 1000 {
			t.Fatalf("published redo = %d, want 1000", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PublishRedoBarrier did not complete after the writer finished")
	}
}

// TestFPIWatermarkSurvivesEviction pins the eviction fix: a page already
// imaged in the current epoch must NOT re-image after its slot is evicted
// and the page is reloaded (observed storm: one hot sys-catalog page imaged
// 19k times = 97.5% of a regress run's WAL). The watermark must ride the
// evictedImageLSN stash across the slot generation.
func TestFPIWatermarkSurvivesEviction(t *testing.T) {
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
	// Slots:2 so pinning two other pages evicts the target.
	pool, err := NewPool(mgr, PoolConfig{
		Slots:          2,
		LogPageImage:   logFPI,
		FullPageWrites: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 603, Fork: MainFork}
	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	s.Lock()
	s.Page()[300] = 1
	pool.MarkDirty(s) // first touch: image #1
	s.Unlock()
	pool.Unpin(s)
	if images != 1 {
		t.Fatalf("first touch: images=%d, want 1", images)
	}

	// Evict block 0 by cycling two other pages through the 2-slot pool.
	for i := 0; i < 2; i++ {
		s2, _, err := pool.PinNew(rel)
		if err != nil {
			t.Fatal(err)
		}
		s2.Lock()
		s2.Page()[300] = byte(i)
		pool.MarkDirty(s2)
		s2.Unlock()
		pool.Unpin(s2)
	}

	// Reload block 0 and write again: same epoch, image already in WAL —
	// must NOT image again.
	s3, err := pool.Pin(BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(s3)
	imagesBefore := images
	s3.Lock()
	s3.Page()[301] = 1
	if err := pool.MarkDirtyChangeRecord(s3, func() (LSN, error) {
		nextLSN++
		return nextLSN, nil
	}); err != nil {
		t.Fatal(err)
	}
	s3.Unlock()
	// The two PinNew extends imaged their own first touches; only the
	// RELOADED page must not have re-imaged.
	if images != imagesBefore {
		t.Fatalf("reloaded page re-imaged after eviction: images=%d, want %d (watermark lost)", images, imagesBefore)
	}

	// After a redo publication above the watermark, the next write must
	// image again (epoch change is the only re-arm).
	pool.PublishRedoRecPtr(uint64(nextLSN) + 10)
	nextLSN += 20
	s3.Lock()
	s3.Page()[302] = 1
	if err := pool.MarkDirtyChangeRecord(s3, func() (LSN, error) {
		nextLSN++
		return nextLSN, nil
	}); err != nil {
		t.Fatal(err)
	}
	s3.Unlock()
	if images != imagesBefore+1 {
		t.Fatalf("post-publication write: images=%d, want %d", images, imagesBefore+1)
	}
}
