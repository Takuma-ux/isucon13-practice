package main

import (
	"context"
	"sync"
	"testing"
)

// Microbench: cost of a full cache HIT through request + process layers (no DB).
func seedProcessCachesForBench(n int) {
	for i := 1; i <= n; i++ {
		id := int64(i)
		putLivestreamShellCache(&LivestreamShell{
			ID: id, OwnerUserID: id, Title: "t", tagsResolved: true,
		})
		writeThroughUserProfileCache(&User{ID: id, Name: "u", IconHash: "h"})
	}
}

func BenchmarkProcessCacheHit_LivestreamShellOnly(b *testing.B) {
	putLivestreamShellCache(&LivestreamShell{ID: 1, OwnerUserID: 1, Title: "t", tagsResolved: true})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = getLivestreamShellFromProcessCache(1)
	}
}

func BenchmarkProcessCacheHit_UserProfileOnly(b *testing.B) {
	writeThroughUserProfileCache(&User{ID: 1, Name: "u", IconHash: "h"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = getUserProfileFromProcessCache(1)
	}
}

// Simulates fill path: request cache miss then process cache hit (typical warm request).
func BenchmarkProcessCacheHit_RequestThenProcessUser(b *testing.B) {
	writeThroughUserProfileCache(&User{ID: 42, Name: "u", IconHash: "h"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := withResponseCache(context.Background())
		if u, ok := getUserFromCache(ctx, 42); ok {
			_ = u
			continue
		}
		if u, ok := getUserProfileFromProcessCache(42); ok {
			cacheUser(ctx, 42, u)
		}
	}
}

// Many goroutines, distinct keys — RWMutex should allow parallel RLock.
func BenchmarkProcessCacheParallel_DistinctKeys(b *testing.B) {
	const nKeys = 256
	seedProcessCachesForBench(nKeys)
	b.SetParallelism(100)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			id := int64((i % nKeys) + 1)
			i++
			_, _ = getLivestreamShellFromProcessCache(id)
			_, _ = getUserProfileFromProcessCache(id)
		}
	})
}

// Hot key: all goroutines same ID — still read locks but same map entry.
func BenchmarkProcessCacheParallel_SameKey(b *testing.B) {
	putLivestreamShellCache(&LivestreamShell{ID: 1, OwnerUserID: 1, Title: "t", tagsResolved: true})
	writeThroughUserProfileCache(&User{ID: 1, Name: "u", IconHash: "h"})
	b.SetParallelism(100)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = getLivestreamShellFromProcessCache(1)
			_, _ = getUserProfileFromProcessCache(1)
		}
	})
}

// singleflight.Do on cache hit should be cheap (double-check path only when miss).
func BenchmarkProcessCacheHit_CoalescedUserFastPath(b *testing.B) {
	writeThroughUserProfileCache(&User{ID: 7, Name: "u", IconHash: "h"})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// coalescedLoadUserProfile returns immediately from process cache; no flight.Do work on hit.
		_, _ = coalescedLoadUserProfile(ctx, nil, 7)
	}
}

func TestProcessCacheBenchSmoke(t *testing.T) {
	var wg sync.WaitGroup
	seedProcessCachesForBench(4)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = getUserProfileFromProcessCache(int64(j%4 + 1))
			}
		}()
	}
	wg.Wait()
}
