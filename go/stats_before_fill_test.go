package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Proves stats hooks run after durable INSERT even when response fill fails (POST still errors).
func TestStatsUpdatedWhenPostLivecommentFillFails(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()

	const ls = 999
	const tip = int64(77)
	ownerID := statsLiveOwners[ls]
	if ownerID == 0 {
		t.Fatal("missing livestream owner in stats maps")
	}
	lsStats := statsLivestreams[ls]
	if lsStats == nil {
		t.Fatal("missing livestream stats")
	}
	baseComments := lsStats.comments.Load()
	baseTips := lsStats.tips.Load()
	basePayment := statsPaymentTotalTips.Load()

	var commenterID int64
	if err := dbConn.GetContext(ctx, &commenterID, `SELECT id FROM users WHERE id != ? ORDER BY id LIMIT 1`, ownerID); err != nil {
		t.Fatalf("commenter: %v", err)
	}

	postLivecommentFillOverride = func(context.Context, readQueryer, LivecommentModel, LivestreamModel) (Livecomment, error) {
		return Livecomment{}, errors.New("test forced fill failure")
	}
	t.Cleanup(func() { postLivecommentFillOverride = nil })

	marker := fmt.Sprintf("fillfail%d", time.Now().UnixNano())
	_, err := postLivecommentWithDeadlockRetry(ctx, commenterID, ls, &PostLivecommentRequest{
		Comment: marker,
		Tip:     tip,
	})
	if err == nil {
		t.Fatal("expected fill failure")
	}
	if !errors.Is(err, errPostLivecommentFill) {
		t.Fatalf("expected errPostLivecommentFill, got %v", err)
	}

	if lsStats.comments.Load() != baseComments+1 {
		t.Fatalf("livestream comments: got %d want %d", lsStats.comments.Load(), baseComments+1)
	}
	if lsStats.tips.Load() != baseTips+tip {
		t.Fatalf("livestream tips: got %d want %d", lsStats.tips.Load(), baseTips+tip)
	}
	if statsPaymentTotalTips.Load() != basePayment+tip {
		t.Fatalf("payment total: got %d want %d", statsPaymentTotalTips.Load(), basePayment+tip)
	}
}

func TestStatsUpdatedWhenPostReactionFillFails(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()

	const ls = 999
	const emoji = "👍"
	ownerID := statsLiveOwners[ls]
	if ownerID == 0 {
		t.Fatal("missing livestream owner in stats maps")
	}
	lsStats := statsLivestreams[ls]
	if lsStats == nil {
		t.Fatal("missing livestream stats")
	}
	baseReactions := lsStats.reactions.Load()

	var reactorID int64
	if err := dbConn.GetContext(ctx, &reactorID, `SELECT id FROM users WHERE id != ? ORDER BY id LIMIT 1`, ownerID); err != nil {
		t.Fatalf("reactor: %v", err)
	}

	postReactionFillOverride = func(context.Context, readQueryer, ReactionModel) (Reaction, error) {
		return Reaction{}, errors.New("test forced fill failure")
	}
	t.Cleanup(func() { postReactionFillOverride = nil })

	_, err := postReaction(ctx, reactorID, ls, emoji)
	if err == nil {
		t.Fatal("expected fill failure")
	}
	if !strings.Contains(err.Error(), "failed to fill reaction") {
		t.Fatalf("unexpected error: %v", err)
	}

	if lsStats.reactions.Load() != baseReactions+1 {
		t.Fatalf("livestream reactions: got %d want %d", lsStats.reactions.Load(), baseReactions+1)
	}
}

func TestStatsUpdatedWhenPostLivecommentReportFillFails(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()

	const ls = 999
	ownerID := statsLiveOwners[ls]
	if ownerID == 0 {
		t.Fatal("missing livestream owner in stats maps")
	}
	lsStats := statsLivestreams[ls]
	if lsStats == nil {
		t.Fatal("missing livestream stats")
	}
	baseReports := lsStats.reports.Load()

	var reporterID int64
	var livecommentID int64
	if err := dbConn.GetContext(ctx, &livecommentID, `SELECT id FROM livecomments WHERE livestream_id = ? ORDER BY id LIMIT 1`, ls); err != nil {
		t.Fatalf("livecomment: %v", err)
	}
	if err := dbConn.GetContext(ctx, &reporterID, `SELECT id FROM users WHERE id != ? ORDER BY id LIMIT 1`, ownerID); err != nil {
		t.Fatalf("reporter: %v", err)
	}

	postLivecommentReportFillOverride = func(context.Context, readQueryer, LivecommentReportModel, LivecommentModel, LivestreamModel) (LivecommentReport, error) {
		return LivecommentReport{}, errors.New("test forced fill failure")
	}
	t.Cleanup(func() { postLivecommentReportFillOverride = nil })

	_, err := postLivecommentReport(ctx, reporterID, ls, int(livecommentID))
	if err == nil {
		t.Fatal("expected fill failure")
	}
	if !strings.Contains(err.Error(), "failed to fill livecomment report") {
		t.Fatalf("unexpected error: %v", err)
	}

	if lsStats.reports.Load() != baseReports+1 {
		t.Fatalf("livestream reports: got %d want %d", lsStats.reports.Load(), baseReports+1)
	}
}
