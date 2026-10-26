package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func assertPaymentTotalTipsMatch(t *testing.T, ctx context.Context, label string) {
	sqlTotal, err := paymentTotalTipsFromSQL(ctx, dbConn)
	if err != nil {
		t.Fatalf("%s: global SUM(tip): %v", label, err)
	}
	grouped, err := paymentTotalTipsFromSQLGrouped(ctx, dbConn)
	if err != nil {
		t.Fatalf("%s: grouped SUM(tip): %v", label, err)
	}
	memTotal := statsPaymentTotalTips.Load()
	t.Logf("checkpoint %q: sql_SUM(tip)=%d sql_grouped_sum=%d mem_payment_total=%d",
		label, sqlTotal, grouped, memTotal)
	if grouped != sqlTotal {
		t.Fatalf("%s: grouped sum %d != global SUM %d", label, grouped, sqlTotal)
	}
	if memTotal != sqlTotal {
		t.Fatalf("%s: in-memory payment total %d != sql SUM %d", label, memTotal, sqlTotal)
	}
}

// Exercises production hooks (postLivecommentWithDeadlockRetry, moderateLivestream) and POST /api/initialize rebuild.
func TestPaymentTotalTipsGroundTruthAfterEachStep(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()

	if err := rebuildStatsFromDB(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	assertPaymentTotalTipsMatch(t, ctx, "after rebuildStatsFromDB")

	const ls = 999
	var ownerID int64
	if err := dbConn.GetContext(ctx, &ownerID, `SELECT user_id FROM livestreams WHERE id = ?`, ls); err != nil {
		t.Fatalf("livestream owner: %v", err)
	}
	var commenterID int64
	if err := dbConn.GetContext(ctx, &commenterID, `SELECT id FROM users WHERE id != ? ORDER BY id LIMIT 1`, ownerID); err != nil {
		t.Fatalf("commenter: %v", err)
	}

	marker := fmt.Sprintf("paygt%d", time.Now().UnixNano())

	_, err := postLivecommentWithDeadlockRetry(ctx, commenterID, ls, &PostLivecommentRequest{
		Comment: fmt.Sprintf("step1-%s", marker),
		Tip:     1000,
	})
	if err != nil {
		t.Fatalf("post tip 1000: %v", err)
	}
	assertPaymentTotalTipsMatch(t, ctx, "after post tip=1000")

	_, err = postLivecommentWithDeadlockRetry(ctx, commenterID, ls, &PostLivecommentRequest{
		Comment: fmt.Sprintf("step2-%s", marker),
		Tip:     500,
	})
	if err != nil {
		t.Fatalf("post tip 500: %v", err)
	}
	assertPaymentTotalTipsMatch(t, ctx, "after post tip=500")

	_, err = postLivecommentWithDeadlockRetry(ctx, commenterID, ls, &PostLivecommentRequest{
		Comment: fmt.Sprintf("step3-delete-me %s marker", marker),
		Tip:     300,
	})
	if err != nil {
		t.Fatalf("post tip 300 (moderate target): %v", err)
	}
	assertPaymentTotalTipsMatch(t, ctx, "after post tip=300 (pre-moderate)")

	_, _, err = moderateLivestream(ctx, ownerID, ls, "marker")
	if err != nil {
		t.Fatalf("moderate: %v", err)
	}
	assertPaymentTotalTipsMatch(t, ctx, "after moderate deletes tipped comment")

	prependFakePdnsutilToPATH(t)
	e := echo.New()
	e.POST("/api/initialize", initializeHandler)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/initialize", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/initialize status %d body=%s", rec.Code, rec.Body.String())
	}
	assertPaymentTotalTipsMatch(t, ctx, "after POST /api/initialize (fresh seed)")
}
