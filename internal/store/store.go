// Package store keeps the most recent frames of each product in memory.
package store

import (
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/vmaltarello/radarpoint/internal/raster"
)

// Frame is one product instant, decoded and ready to be queried.
type Frame struct {
	Product string
	Time    time.Time     // nominal time of the data, UTC
	Period  time.Duration // product update period
	Key     string        // object key on the Radar-DPC bucket
	Size    int           // size of the downloaded file in bytes
	Fetched time.Time     // when the file was downloaded
	Grid    *raster.GeoTIFF
}

// Store holds, for each product, the last few frames ordered by time.
// It is safe for concurrent use. Frames must not be modified once added.
type Store struct {
	mu     sync.RWMutex
	frames map[string][]*Frame
}

// New returns an empty store.
func New() *Store {
	return &Store{frames: map[string][]*Frame{}}
}

// Add inserts a frame. It returns false if a frame with the same product and
// time is already stored.
func (s *Store) Add(f *Frame) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.frames[f.Product]
	i := sort.Search(len(list), func(i int) bool { return !list[i].Time.Before(f.Time) })
	if i < len(list) && list[i].Time.Equal(f.Time) {
		return false
	}
	s.frames[f.Product] = slices.Insert(list, i, f)
	return true
}

// DropBefore removes the frames of product older than t.
func (s *Store) DropBefore(product string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames[product] = slices.DeleteFunc(s.frames[product], func(f *Frame) bool { return f.Time.Before(t) })
}

// Has reports whether the frame of product at time t is stored.
func (s *Store) Has(product string, t time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, f := range s.frames[product] {
		if f.Time.Equal(t) {
			return true
		}
	}
	return false
}

// Latest returns the most recent frame of a product, or nil.
func (s *Store) Latest(product string) *Frame {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.frames[product]
	if len(list) == 0 {
		return nil
	}
	return list[len(list)-1]
}

// Frames returns the stored frames of a product, oldest first.
func (s *Store) Frames(product string) []*Frame {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.frames[product])
}
