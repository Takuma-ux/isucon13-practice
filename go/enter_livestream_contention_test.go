package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

)

// enterLivestreamOnce mirrors enterLivestreamHandler txn body (no deadlock retry).
func enterLivestreamOnce(ctx context.Context, userID int64, livestreamID int64) error {
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	viewer := LivestreamViewerModel{
		UserID:       userID,
		LivestreamID: livestreamID,
		CreatedAt:    time.Now().Unix(),
	}
	if _, err := tx.NamedExecContext(ctx,
		"INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES(:user_id, :livestream_id, :created_at)",
		viewer); err != nil {
		return err
	}
	return tx.Commit()
}

func TestEnterExitContentionObservedDeadlocks(t *testing.T) {
	if os.Getenv("ISUCON13_MODERATE_CONVERGE_TEST") != "1" {
		t.Skip("set ISUCON13_MODERATE_CONVERGE_TEST=1")
	}
	db := testDB(t)
	dbConn = db
	ctx := context.Background()
	const ls = int64(999)

	var userIDs []int64
	if err := db.SelectContext(ctx, &userIDs, `SELECT id FROM users ORDER BY id LIMIT 80`); err != nil {
		t.Fatalf("users: %v", err)
	}

	var enterDeadlock, enterOther, exitDeadlock, exitOK, enterOK atomic.Int64
	deadline := time.Now().Add(3 * time.Second)
	var wg sync.WaitGroup

	for _, uid := range userIDs {
		wg.Add(1)
		go func(userID int64) {
			defer wg.Done()
			for time.Now().Before(deadline) {
				if err := enterLivestreamOnce(ctx, userID, ls); err != nil {
					if isMySQLDeadlock(err) {
						enterDeadlock.Add(1)
					} else {
						enterOther.Add(1)
					}
				} else {
					enterOK.Add(1)
				}
			}
		}(uid)
	}

	for _, uid := range userIDs {
		wg.Add(1)
		go func(userID int64) {
			defer wg.Done()
			for time.Now().Before(deadline) {
				err := withMySQLDeadlockRetry(func() error {
					return exitLivestream(ctx, userID, int(ls))
				})
				if err != nil {
					if isMySQLDeadlock(err) {
						exitDeadlock.Add(1)
					}
				} else {
					exitOK.Add(1)
				}
			}
		}(uid)
	}

	wg.Wait()
	t.Logf("enterOK=%d enterDeadlock=%d enterOther=%d exitOK=%d exitDeadlock=%d",
		enterOK.Load(), enterDeadlock.Load(), enterOther.Load(), exitOK.Load(), exitDeadlock.Load())
	if enterDeadlock.Load() == 0 && enterOther.Load() == 0 {
		t.Log("no enter failures observed in this window (deadlock risk may still exist at higher load)")
	}
}
