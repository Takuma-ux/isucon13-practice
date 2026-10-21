package main

import (
	"context"
	"testing"
)

func benchGetUserContext(b *testing.B, nUsers int, nLookups int) {
	ctx := withResponseCache(context.Background())
	for i := 0; i < nUsers; i++ {
		u := &User{ID: int64(i + 1), Name: "u"}
		cacheUser(ctx, int64(i+1), u)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < nLookups; j++ {
			id := int64((j % nUsers) + 1)
			_, _ = getUserFromCache(ctx, id)
		}
	}
}

func benchGetUserExplicit(b *testing.B, nUsers int, nLookups int) {
	rc := &responseCache{
		users:       make(map[int64]*User, nUsers),
		livestreams: make(map[int64]*Livestream),
	}
	for i := 0; i < nUsers; i++ {
		rc.users[int64(i+1)] = &User{ID: int64(i + 1), Name: "u"}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < nLookups; j++ {
			id := int64((j % nUsers) + 1)
			_, _ = rc.users[id]
		}
	}
}

// ~100 comments, ~15 distinct users: fillUsersByIDs does one cache read per unique ID
// plus fillLivestreamsResponseWithOwners one cache read per uncached livestream row.
func BenchmarkResponseCache_UserLookup_Context_15x15(b *testing.B) {
	benchGetUserContext(b, 15, 15)
}

func BenchmarkResponseCache_UserLookup_Explicit_15x15(b *testing.B) {
	benchGetUserExplicit(b, 15, 15)
}

func BenchmarkResponseCache_UserLookup_Context_15x100(b *testing.B) {
	benchGetUserContext(b, 15, 100)
}

func BenchmarkResponseCache_UserLookup_Explicit_15x100(b *testing.B) {
	benchGetUserExplicit(b, 15, 100)
}
