package main

import (
	"context"
	"testing"
	"time"
)

func statsUserViewersCount(userID int64) int64 {
	statsRebuildMu.RLock()
	u := statsUsers[userID]
	statsRebuildMu.RUnlock()
	if u == nil {
		return 0
	}
	return u.viewers.Load()
}

func enterLivestreamWithStats(ctx context.Context, userID int64, livestreamID int64) error {
	viewer := LivestreamViewerModel{
		UserID:       userID,
		LivestreamID: livestreamID,
		CreatedAt:    time.Now().Unix(),
	}
	if _, err := dbConn.NamedExecContext(ctx,
		"INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES(:user_id, :livestream_id, :created_at)",
		viewer); err != nil {
		return err
	}
	statsOnEnter(livestreamID)
	return nil
}

func TestExitLivestreamStatsRowsAffected(t *testing.T) {
	requireSharedIntegrationDB(t)
	ctx := context.Background()
	if err := rebuildStatsFromDB(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	var livestreamID int64
	if err := dbConn.GetContext(ctx, &livestreamID, "SELECT id FROM livestreams ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("livestream: %v", err)
	}
	var userID int64
	if err := dbConn.GetContext(ctx, &userID, "SELECT id FROM users ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("user: %v", err)
	}

	t.Cleanup(func() {
		_, _ = dbConn.ExecContext(ctx,
			"DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?",
			userID, livestreamID)
		if err := rebuildStatsFromDB(ctx); err != nil {
			t.Logf("cleanup rebuild: %v", err)
		}
	})

	beforeLS, err := livestreamStatisticsFromMemory(livestreamID)
	if err != nil {
		t.Fatalf("baseline livestream stats: %v", err)
	}
	ownerID := statsLiveOwners[livestreamID]
	beforeOwnerViewers := statsUserViewersCount(ownerID)

	if err := enterLivestreamWithStats(ctx, userID, livestreamID); err != nil {
		t.Fatalf("enter 1: %v", err)
	}
	if err := enterLivestreamWithStats(ctx, userID, livestreamID); err != nil {
		t.Fatalf("enter 2: %v", err)
	}

	var dbRows int
	if err := dbConn.GetContext(ctx, &dbRows,
		"SELECT COUNT(*) FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?",
		userID, livestreamID); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if dbRows != 2 {
		t.Fatalf("expected 2 viewer rows before exit, got %d", dbRows)
	}

	if err := exitLivestream(ctx, userID, int(livestreamID)); err != nil {
		t.Fatalf("exit: %v", err)
	}

	if err := dbConn.GetContext(ctx, &dbRows,
		"SELECT COUNT(*) FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?",
		userID, livestreamID); err != nil {
		t.Fatalf("count rows after exit: %v", err)
	}
	if dbRows != 0 {
		t.Fatalf("expected 0 viewer rows after exit, got %d", dbRows)
	}

	afterLS, err := livestreamStatisticsFromMemory(livestreamID)
	if err != nil {
		t.Fatalf("after livestream stats: %v", err)
	}
	if afterLS.ViewersCount != beforeLS.ViewersCount {
		t.Fatalf("livestream viewers after double-enter single-exit: got %d want baseline %d",
			afterLS.ViewersCount, beforeLS.ViewersCount)
	}

	if statsUserViewersCount(ownerID) != beforeOwnerViewers {
		t.Fatalf("owner viewers after double-enter single-exit: got %d want baseline %d",
			statsUserViewersCount(ownerID), beforeOwnerViewers)
	}
}

func TestExitLivestreamStatsZeroRowsRemoved(t *testing.T) {
	requireSharedIntegrationDB(t)
	ctx := context.Background()
	if err := rebuildStatsFromDB(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	var livestreamID int64
	if err := dbConn.GetContext(ctx, &livestreamID, "SELECT id FROM livestreams ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("livestream: %v", err)
	}
	var userID int64
	if err := dbConn.GetContext(ctx, &userID, "SELECT id FROM users WHERE id NOT IN (SELECT user_id FROM livestream_viewers_history WHERE livestream_id = ?) ORDER BY id LIMIT 1", livestreamID); err != nil {
		t.Fatalf("user without viewer row: %v", err)
	}

	beforeLS, err := livestreamStatisticsFromMemory(livestreamID)
	if err != nil {
		t.Fatalf("baseline livestream stats: %v", err)
	}
	ownerID := statsLiveOwners[livestreamID]
	beforeOwnerViewers := statsUserViewersCount(ownerID)

	if err := exitLivestream(ctx, userID, int(livestreamID)); err != nil {
		t.Fatalf("exit with no rows: %v", err)
	}

	afterLS, err := livestreamStatisticsFromMemory(livestreamID)
	if err != nil {
		t.Fatalf("after livestream stats: %v", err)
	}
	if afterLS.ViewersCount != beforeLS.ViewersCount {
		t.Fatalf("livestream viewers after noop exit: got %d want unchanged %d",
			afterLS.ViewersCount, beforeLS.ViewersCount)
	}

	if statsUserViewersCount(ownerID) != beforeOwnerViewers {
		t.Fatalf("owner viewers after noop exit: got %d want unchanged %d",
			statsUserViewersCount(ownerID), beforeOwnerViewers)
	}
}
