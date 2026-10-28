package main

import (
	"context"
	"database/sql"
	"sync"
)

var (
	themeCacheMu    sync.RWMutex
	themeCache      = make(map[int64]ThemeModel) // user_id -> theme row
	themeCacheEpoch = make(map[int64]uint64)
)

func clearThemeCache() {
	themeCacheMu.Lock()
	themeCache = make(map[int64]ThemeModel)
	themeCacheEpoch = make(map[int64]uint64)
	themeCacheMu.Unlock()
}

func snapshotThemeCacheEpoch(userID int64) uint64 {
	themeCacheMu.RLock()
	epoch := themeCacheEpoch[userID]
	themeCacheMu.RUnlock()
	return epoch
}

func invalidateThemeCacheUser(userID int64) {
	themeCacheMu.Lock()
	delete(themeCache, userID)
	themeCacheEpoch[userID]++
	themeCacheMu.Unlock()
}

func putThemeCache(t ThemeModel, fillEpoch uint64) {
	themeCacheMu.Lock()
	defer themeCacheMu.Unlock()
	if themeCacheEpoch[t.UserID] != fillEpoch {
		return
	}
	themeCache[t.UserID] = t
}

func writeThroughThemeCache(t ThemeModel) {
	themeCacheMu.Lock()
	themeCacheEpoch[t.UserID]++
	themeCache[t.UserID] = t
	themeCacheMu.Unlock()
}

func getThemeModelFromCache(userID int64) (ThemeModel, bool) {
	themeCacheMu.RLock()
	t, ok := themeCache[userID]
	themeCacheMu.RUnlock()
	return t, ok
}

func warmThemeCache(ctx context.Context) error {
	var rows []ThemeModel
	if err := dbConn.SelectContext(ctx, &rows, "SELECT id, user_id, dark_mode FROM themes"); err != nil {
		return err
	}
	themeCacheMu.Lock()
	for _, t := range rows {
		themeCache[t.UserID] = t
	}
	themeCacheMu.Unlock()
	return nil
}

// themeForUser returns the theme row for userID, using the epoch-guarded process cache before MySQL.
func themeForUser(ctx context.Context, q readQueryer, userID int64) (ThemeModel, error) {
	if t, ok := getThemeModelFromCache(userID); ok {
		return t, nil
	}

	fillEpoch := snapshotThemeCacheEpoch(userID)

	var t ThemeModel
	if err := q.GetContext(ctx, &t, "SELECT id, user_id, dark_mode FROM themes WHERE user_id = ?", userID); err != nil {
		return ThemeModel{}, err
	}

	putThemeCache(t, fillEpoch)

	if cached, ok := getThemeModelFromCache(userID); ok {
		return cached, nil
	}
	return t, nil
}

func themesForUsers(ctx context.Context, q readQueryer, userIDs []int64) (map[int64]ThemeModel, error) {
	out := make(map[int64]ThemeModel, len(userIDs))
	miss := make([]int64, 0, len(userIDs))
	for _, id := range userIDs {
		if t, ok := getThemeModelFromCache(id); ok {
			out[id] = t
			continue
		}
		miss = append(miss, id)
	}
	if len(miss) == 0 {
		return out, nil
	}
	miss = uniqueInt64(miss)

	var themeRows []ThemeModel
	if err := selectIn(ctx, q, &themeRows, "SELECT id, user_id, dark_mode FROM themes WHERE user_id IN (?)", miss); err != nil {
		return nil, err
	}
	if len(themeRows) != len(miss) {
		return nil, sql.ErrNoRows
	}
	for _, row := range themeRows {
		fillEpoch := snapshotThemeCacheEpoch(row.UserID)
		putThemeCache(row, fillEpoch)
		if cached, ok := getThemeModelFromCache(row.UserID); ok {
			out[row.UserID] = cached
		} else {
			out[row.UserID] = row
		}
	}
	return out, nil
}

func statsLookupUserIDByName(username string) (int64, bool) {
	statsRebuildMu.RLock()
	id, ok := statsUsernames[username]
	statsRebuildMu.RUnlock()
	return id, ok
}
