package kvcache

import "testing"

func TestLRUOrderAndEviction(t *testing.T) {
	c := New(100)
	c.Put("a", 1, 40)
	c.Put("b", 2, 40)
	// Touch a so b becomes the LRU entry.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a missing")
	}
	c.Put("c", 3, 40) // over budget: must evict b (LRU), keep a
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted as LRU")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should have survived (recently used)")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("c should be present")
	}
	if c.Evictions() != 1 {
		t.Fatalf("evictions = %d, want 1", c.Evictions())
	}
	if c.Bytes() != 80 {
		t.Fatalf("bytes = %d, want 80", c.Bytes())
	}
}

func TestOversizeEntryNotStored(t *testing.T) {
	c := New(50)
	c.Put("big", 1, 200)
	if c.Len() != 0 || c.Bytes() != 0 {
		t.Fatalf("oversize entry stored: len=%d bytes=%d", c.Len(), c.Bytes())
	}
	// The failed insert must not have leaked budget.
	c.Put("ok", 2, 40)
	if _, found := c.Get("ok"); !found {
		t.Fatal("normal entry should fit after failed oversize insert")
	}
}

func TestUnlimitedBudget(t *testing.T) {
	c := New(0)
	for i := 0; i < 100; i++ {
		c.Put(string(rune('a'+i%26))+string(rune('0'+i/26)), i, 1<<20)
	}
	if c.Evictions() != 0 {
		t.Fatalf("unlimited budget must never evict, got %d", c.Evictions())
	}
}

func TestOverwriteReleasesOldSize(t *testing.T) {
	c := New(100)
	c.Put("k", 1, 60)
	c.Put("k", 2, 30) // overwrite with smaller entry
	if c.Bytes() != 30 {
		t.Fatalf("bytes = %d, want 30 after overwrite", c.Bytes())
	}
	v, _ := c.Get("k")
	if v.(int) != 2 {
		t.Fatalf("value = %v, want 2", v)
	}
	if c.Len() != 1 {
		t.Fatalf("len = %d, want 1", c.Len())
	}
}

func TestSharedBudgetLocalEviction(t *testing.T) {
	b := NewBudget(100)
	c1 := NewShared(b)
	c2 := NewShared(b)
	c1.Put("x", 1, 60)
	c2.Put("y", 2, 30)
	// c2 needs 40 more: budget is 90/100 used, so it must evict its own
	// LRU (y) — never c1's entry.
	c2.Put("z", 3, 40)
	if _, ok := c1.Get("x"); !ok {
		t.Fatal("shared-budget eviction stole a sibling cache's entry")
	}
	if _, ok := c2.Get("y"); ok {
		t.Fatal("c2 should have evicted its own LRU entry")
	}
	if _, ok := c2.Get("z"); !ok {
		t.Fatal("z should be present")
	}
	if got := b.Used(); got != 100 {
		t.Fatalf("budget used = %d, want 100", got)
	}
}

func TestClearReleasesBudgetWithoutCountingEvictions(t *testing.T) {
	b := NewBudget(100)
	c := NewShared(b)
	c.Put("a", 1, 40)
	c.Put("b", 2, 40)
	c.Clear()
	if c.Len() != 0 || c.Bytes() != 0 {
		t.Fatalf("clear left len=%d bytes=%d", c.Len(), c.Bytes())
	}
	if b.Used() != 0 {
		t.Fatalf("clear did not release budget: used=%d", b.Used())
	}
	if c.Evictions() != 0 {
		t.Fatalf("clear must not count as eviction, got %d", c.Evictions())
	}
}
