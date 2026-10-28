package main

import (
	"context"
	"testing"
)

func TestWarmThemeCacheMatchesDB(t *testing.T) {
	requireSharedIntegrationDB(t)
	ctx := context.Background()
	clearThemeCache()

	var dbCount int
	if err := dbConn.GetContext(ctx, &dbCount, "SELECT COUNT(*) FROM themes"); err != nil {
		t.Fatalf("count themes: %v", err)
	}
	if err := warmThemeCache(ctx); err != nil {
		t.Fatalf("warmThemeCache: %v", err)
	}

	themeCacheMu.RLock()
	cacheLen := len(themeCache)
	themeCacheMu.RUnlock()
	if cacheLen != dbCount {
		t.Fatalf("cache rows %d want db count %d", cacheLen, dbCount)
	}
}

func TestThemeForUserHitAfterWarm(t *testing.T) {
	requireSharedIntegrationDB(t)
	ctx := context.Background()
	clearThemeCache()
	if err := warmThemeCache(ctx); err != nil {
		t.Fatalf("warmThemeCache: %v", err)
	}

	var userID int64
	if err := dbConn.GetContext(ctx, &userID, "SELECT user_id FROM themes ORDER BY user_id LIMIT 1"); err != nil {
		t.Fatalf("pick theme user: %v", err)
	}

	tm, err := themeForUser(ctx, dbConn, userID)
	if err != nil {
		t.Fatalf("themeForUser: %v", err)
	}
	if tm.UserID != userID || tm.ID == 0 {
		t.Fatalf("unexpected theme %+v for user %d", tm, userID)
	}
}
