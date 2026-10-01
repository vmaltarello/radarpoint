package store

import (
	"testing"
	"time"
)

func TestAddInOrderAndDrop(t *testing.T) {
	s := New()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }

	// Out of order inserts, as when a gap is filled after a newer frame.
	for _, m := range []int{10, 0, 5, 20, 15} {
		if !s.Add(&Frame{Product: "SRI", Time: at(m)}) {
			t.Fatalf("Add %d rejected", m)
		}
	}
	if s.Add(&Frame{Product: "SRI", Time: at(20)}) {
		t.Error("duplicate accepted")
	}
	s.DropBefore("SRI", at(10))
	frames := s.Frames("SRI")
	if len(frames) != 3 {
		t.Fatalf("%d frames, want 3", len(frames))
	}
	for i, m := range []int{10, 15, 20} {
		if !frames[i].Time.Equal(at(m)) {
			t.Errorf("frame %d at %v, want %v", i, frames[i].Time, at(m))
		}
	}
	if l := s.Latest("SRI"); !l.Time.Equal(at(20)) {
		t.Errorf("Latest = %v", l.Time)
	}
	if !s.Has("SRI", at(15)) || s.Has("SRI", at(0)) {
		t.Error("Has reports dropped or kept frames wrongly")
	}
}

func TestEmptyProduct(t *testing.T) {
	s := New()
	if s.Latest("POH") != nil || len(s.Frames("POH")) != 0 {
		t.Fatal("empty store returned frames")
	}
}
