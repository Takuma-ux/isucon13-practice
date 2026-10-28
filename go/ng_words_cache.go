package main

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

type ngWordsCacheKey struct {
	ownerUserID  int64
	livestreamID int64
}

var (
	ngWordsCacheMu    sync.RWMutex
	ngWordsCache      = make(map[ngWordsCacheKey][]NGWord)
	ngWordsCacheEpoch = make(map[ngWordsCacheKey]uint64)
)

func clearNGWordsCache() {
	ngWordsCacheMu.Lock()
	ngWordsCache = make(map[ngWordsCacheKey][]NGWord)
	ngWordsCacheEpoch = make(map[ngWordsCacheKey]uint64)
	ngWordsCacheMu.Unlock()
}

func invalidateNGWordsCacheForOwnerLivestream(ownerUserID, livestreamID int64) {
	key := ngWordsCacheKey{ownerUserID: ownerUserID, livestreamID: livestreamID}
	ngWordsCacheMu.Lock()
	delete(ngWordsCache, key)
	ngWordsCacheEpoch[key]++
	ngWordsCacheMu.Unlock()
}

// mergeNGWordIntoCacheAfterModerate prepends the new word when the cache is warm (avoids SELECT on next spam check);
// if cold, bumps epoch only (same as invalidate) so the next load refetches the full list from MySQL.
func mergeNGWordIntoCacheAfterModerate(ownerUserID, livestreamID int64, word NGWord) {
	key := ngWordsCacheKey{ownerUserID: ownerUserID, livestreamID: livestreamID}
	ngWordsCacheMu.Lock()
	defer ngWordsCacheMu.Unlock()
	ngWordsCacheEpoch[key]++
	if existing, ok := ngWordsCache[key]; ok {
		combined := append([]NGWord{word}, existing...)
		ngWordsCache[key] = cloneNGWords(combined)
		return
	}
	delete(ngWordsCache, key)
}

func snapshotNGWordsCacheEpoch(key ngWordsCacheKey) uint64 {
	ngWordsCacheMu.RLock()
	epoch := ngWordsCacheEpoch[key]
	ngWordsCacheMu.RUnlock()
	return epoch
}

func putNGWordsCache(key ngWordsCacheKey, words []NGWord, fillEpoch uint64) {
	stored := cloneNGWords(words)
	ngWordsCacheMu.Lock()
	defer ngWordsCacheMu.Unlock()
	if ngWordsCacheEpoch[key] != fillEpoch {
		return
	}
	ngWordsCache[key] = stored
}

func cloneNGWords(in []NGWord) []NGWord {
	if len(in) == 0 {
		return []NGWord{}
	}
	out := make([]NGWord, len(in))
	copy(out, in)
	return out
}

// ngWordsForOwnerLivestream returns NG words for spam check / GET ngwords (owner user_id + livestream_id).
func ngWordsForOwnerLivestream(ctx context.Context, q readQueryer, ownerUserID, livestreamID int64) ([]NGWord, error) {
	return coalescedNGWordsForOwnerLivestream(ctx, q, ownerUserID, livestreamID)
}

func loadNGWordsForOwnerLivestream(ctx context.Context, q readQueryer, ownerUserID, livestreamID int64) ([]NGWord, error) {
	key := ngWordsCacheKey{ownerUserID: ownerUserID, livestreamID: livestreamID}

	fillEpoch := snapshotNGWordsCacheEpoch(key)

	var rows []NGWord
	if err := q.SelectContext(ctx, &rows,
		"SELECT id, user_id, livestream_id, word, created_at FROM ng_words WHERE user_id = ? AND livestream_id = ? ORDER BY created_at DESC",
		ownerUserID, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	putNGWordsCache(key, rows, fillEpoch)

	ngWordsCacheMu.RLock()
	if cached, ok := ngWordsCache[key]; ok {
		ngWordsCacheMu.RUnlock()
		return cloneNGWords(cached), nil
	}
	ngWordsCacheMu.RUnlock()
	return cloneNGWords(rows), nil
}
