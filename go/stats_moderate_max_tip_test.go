package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func seedLivestreamCommentsForMaxTipTest(t *testing.T, ctx context.Context, lsID int64, ownerID int64, tips []int64) {
	_, err := dbConn.ExecContext(ctx, "DELETE FROM livecomments WHERE livestream_id = ?", lsID)
	if err != nil {
		t.Fatal(err)
	}
	for i, tip := range tips {
		_, err := dbConn.ExecContext(ctx, `
			INSERT INTO livecomments (user_id, livestream_id, comment, tip, created_at)
			VALUES (?, ?, ?, ?, UNIX_TIMESTAMP())`, ownerID, lsID, fmt.Sprintf("c%d", i), tip)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := rebuildStatsFromDB(ctx); err != nil {
		t.Fatal(err)
	}
}

// MAX(tip) is retried on deadlock; success after transient failures still updates max_tip.
func TestModerateDeletedMaxTipRefreshRetriesOnDeadlock(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()
	const lsID int64 = 1
	ownerID := statsLiveOwners[lsID]
	if ownerID == 0 {
		t.Fatal("missing livestream owner in stats maps")
	}
	seedLivestreamCommentsForMaxTipTest(t, ctx, lsID, ownerID, []int64{30, 90})

	ls := statsLivestreams[lsID]
	if ls == nil {
		t.Fatal("missing livestream stats")
	}
	if ls.maxTip != 90 {
		t.Fatalf("seed max_tip: got %d want 90", ls.maxTip)
	}

	attempts := 0
	statsLivestreamMaxTipFromDB = func(ctx context.Context, livestreamID int64) (int64, error) {
		attempts++
		if attempts < 3 {
			return 0, &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"}
		}
		return queryLivestreamMaxTipFromDB(ctx, livestreamID)
	}
	t.Cleanup(func() { statsLivestreamMaxTipFromDB = nil })

	// Delete the max-tip comment (90); remaining max in DB is 30.
	_, err := dbConn.ExecContext(ctx, `
		DELETE FROM livecomments WHERE livestream_id = ? AND tip = 90 LIMIT 1`, lsID)
	if err != nil {
		t.Fatal(err)
	}

	beforeFails := statsModerateMaxTipRefreshFailures.Load()
	statsOnModerateDeleted(ctx, lsID, ownerID, moderateDeleteStats{Count: 1, TipSum: 90, MaxDeleted: 90})

	if attempts != 3 {
		t.Fatalf("expected 3 MAX(tip) attempts (2 deadlocks + success), got %d", attempts)
	}
	if statsModerateMaxTipRefreshFailures.Load() != beforeFails {
		t.Fatalf("unexpected max_tip refresh failure counter bump")
	}
	if ls.maxTip != 30 {
		t.Fatalf("max_tip after moderate: got %d want 30", ls.maxTip)
	}
	if ls.tips.Load() != 30 {
		t.Fatalf("tips after moderate: got %d want 30", ls.tips.Load())
	}
}

// When MAX(tip) cannot succeed, counters are still updated but failure is counted and logged (not silent).
func TestModerateDeletedMaxTipRefreshFailureIsDetected(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()
	const lsID int64 = 1
	ownerID := statsLiveOwners[lsID]
	if ownerID == 0 {
		t.Fatal("missing livestream owner in stats maps")
	}
	seedLivestreamCommentsForMaxTipTest(t, ctx, lsID, ownerID, []int64{40, 120})

	ls := statsLivestreams[lsID]
	if ls == nil {
		t.Fatal("missing livestream stats")
	}
	baseTips := ls.tips.Load()
	baseComments := ls.comments.Load()
	basePayment := statsPaymentTotalTips.Load()
	staleMax := ls.maxTip
	if staleMax != 120 {
		t.Fatalf("seed max_tip: got %d want 120", staleMax)
	}

	queryErr := errors.New("test forced MAX(tip) failure")
	statsLivestreamMaxTipFromDB = func(context.Context, int64) (int64, error) {
		return 0, queryErr
	}
	t.Cleanup(func() { statsLivestreamMaxTipFromDB = nil })

	beforeFails := statsModerateMaxTipRefreshFailures.Load()
	statsOnModerateDeleted(ctx, lsID, ownerID, moderateDeleteStats{Count: 1, TipSum: 120, MaxDeleted: 120})

	if statsModerateMaxTipRefreshFailures.Load() != beforeFails+1 {
		t.Fatalf("expected max_tip refresh failure counter +1")
	}
	if ls.maxTip != staleMax {
		t.Fatalf("max_tip should stay stale (%d) when refresh fails, got %d", staleMax, ls.maxTip)
	}
	if ls.tips.Load() != baseTips-120 {
		t.Fatalf("tips: got %d want %d", ls.tips.Load(), baseTips-120)
	}
	if ls.comments.Load() != baseComments-1 {
		t.Fatalf("comments: got %d want %d", ls.comments.Load(), baseComments-1)
	}
	if statsPaymentTotalTips.Load() != basePayment-120 {
		t.Fatalf("payment total: got %d want %d", statsPaymentTotalTips.Load(), basePayment-120)
	}
}
