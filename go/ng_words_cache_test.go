package main

import (
	"testing"
)

func TestMergeNGWordIntoCacheAfterModerateWarmPrepends(t *testing.T) {
	const owner int64 = 1
	const live int64 = 2
	key := ngWordsCacheKey{ownerUserID: owner, livestreamID: live}
	old := NGWord{ID: 10, UserID: owner, LivestreamID: live, Word: "old", CreatedAt: 1}

	ngWordsCacheMu.Lock()
	savedWords := ngWordsCache[key]
	savedEpoch := ngWordsCacheEpoch[key]
	ngWordsCache[key] = []NGWord{old}
	ngWordsCacheEpoch[key] = 5
	ngWordsCacheMu.Unlock()
	t.Cleanup(func() {
		ngWordsCacheMu.Lock()
		if savedWords == nil {
			delete(ngWordsCache, key)
		} else {
			ngWordsCache[key] = savedWords
		}
		ngWordsCacheEpoch[key] = savedEpoch
		ngWordsCacheMu.Unlock()
	})

	newWord := NGWord{ID: 11, UserID: owner, LivestreamID: live, Word: "new", CreatedAt: 2}
	mergeNGWordIntoCacheAfterModerate(owner, live, newWord)

	ngWordsCacheMu.RLock()
	got := ngWordsCache[key]
	epoch := ngWordsCacheEpoch[key]
	ngWordsCacheMu.RUnlock()

	if epoch != 6 {
		t.Fatalf("epoch: got %d want 6", epoch)
	}
	if len(got) != 2 {
		t.Fatalf("len: got %d want 2", len(got))
	}
	if got[0].Word != "new" || got[1].Word != "old" {
		t.Fatalf("order: %+v", got)
	}
}

func TestMergeNGWordIntoCacheAfterModerateColdBumpsEpochOnly(t *testing.T) {
	const owner int64 = 3
	const live int64 = 4
	key := ngWordsCacheKey{ownerUserID: owner, livestreamID: live}

	ngWordsCacheMu.Lock()
	savedWords := ngWordsCache[key]
	savedEpoch := ngWordsCacheEpoch[key]
	delete(ngWordsCache, key)
	ngWordsCacheEpoch[key] = 2
	ngWordsCacheMu.Unlock()
	t.Cleanup(func() {
		ngWordsCacheMu.Lock()
		if savedWords == nil {
			delete(ngWordsCache, key)
		} else {
			ngWordsCache[key] = savedWords
		}
		ngWordsCacheEpoch[key] = savedEpoch
		ngWordsCacheMu.Unlock()
	})

	mergeNGWordIntoCacheAfterModerate(owner, live, NGWord{ID: 1, Word: "x"})

	ngWordsCacheMu.RLock()
	_, ok := ngWordsCache[key]
	epoch := ngWordsCacheEpoch[key]
	ngWordsCacheMu.RUnlock()

	if ok {
		t.Fatal("expected empty cache on cold merge")
	}
	if epoch != 3 {
		t.Fatalf("epoch: got %d want 3", epoch)
	}
}
