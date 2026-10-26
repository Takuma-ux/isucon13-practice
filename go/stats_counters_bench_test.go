package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

// Mirrors production layout (adjacent atomics).
type livestreamStatCountersUnpadded struct {
	reactions atomic.Int64
	tips      atomic.Int64
	comments  atomic.Int64
	viewers   atomic.Int64
	reports   atomic.Int64
}

// One atomic per 64-byte cache line (independent hot writers).
type livestreamStatCountersPadded struct {
	reactions atomic.Int64
	_         [56]byte
	tips atomic.Int64
	_    [56]byte
	comments atomic.Int64
	_        [56]byte
	viewers atomic.Int64
	_       [56]byte
	reports atomic.Int64
}

func TestStatsCounterStructLayout(t *testing.T) {
	t.Logf("unpadded livestreamStatCounters size=%d align=%d", unsafe.Sizeof(livestreamStatCountersUnpadded{}), unsafe.Alignof(livestreamStatCountersUnpadded{}.reactions))
	offReactions := unsafe.Offsetof(livestreamStatCountersUnpadded{}.reactions)
	offTips := unsafe.Offsetof(livestreamStatCountersUnpadded{}.tips)
	offComments := unsafe.Offsetof(livestreamStatCountersUnpadded{}.comments)
	t.Logf("unpadded offsets: reactions=%d tips=%d comments=%d (all atomics share one ~40-byte span → one cache line)", offReactions, offTips, offComments)

	t.Logf("padded size=%d", unsafe.Sizeof(livestreamStatCountersPadded{}))
}

func hammerUnpadded(b *testing.B, field int) {
	var s livestreamStatCountersUnpadded
	const workers = 8
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			f := (field + w) % 5
			go func() {
				defer wg.Done()
				switch f {
				case 0:
					for j := 0; j < 5000; j++ {
						s.reactions.Add(1)
					}
				case 1:
					for j := 0; j < 5000; j++ {
						s.tips.Add(1)
					}
				case 2:
					for j := 0; j < 5000; j++ {
						s.comments.Add(1)
					}
				case 3:
					for j := 0; j < 5000; j++ {
						s.viewers.Add(1)
					}
				default:
					for j := 0; j < 5000; j++ {
						s.reports.Add(1)
					}
				}
			}()
		}
		wg.Wait()
	}
}

func hammerPadded(b *testing.B, field int) {
	var s livestreamStatCountersPadded
	const workers = 8
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			f := (field + w) % 5
			go func() {
				defer wg.Done()
				switch f {
				case 0:
					for j := 0; j < 5000; j++ {
						s.reactions.Add(1)
					}
				case 1:
					for j := 0; j < 5000; j++ {
						s.tips.Add(1)
					}
				case 2:
					for j := 0; j < 5000; j++ {
						s.comments.Add(1)
					}
				case 3:
					for j := 0; j < 5000; j++ {
						s.viewers.Add(1)
					}
				default:
					for j := 0; j < 5000; j++ {
						s.reports.Add(1)
					}
				}
			}()
		}
		wg.Wait()
	}
}

// Simulates postReaction + postLivecomment + enter + report on the same livestream (different counters).
func BenchmarkLivestreamCountersUnpadded_MixedWriters(b *testing.B) {
	hammerUnpadded(b, 0)
}

func BenchmarkLivestreamCountersPadded_MixedWriters(b *testing.B) {
	hammerPadded(b, 0)
}
