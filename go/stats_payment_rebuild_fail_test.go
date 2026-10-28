package main

import (
	"context"
	"testing"
)

// Forces rebuildStatsFromDB to fail after users/livestreams identity reload but before tip aggregation.
func TestRebuildStatsFailureResetsPaymentTotalTips(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()

	const stalePaymentTotal = int64(42424242)
	statsPaymentTotalTips.Store(stalePaymentTotal)

	_, err := dbConn.ExecContext(ctx, "RENAME TABLE reactions TO reactions_rebuild_fail_test")
	if err != nil {
		t.Fatalf("hide reactions table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbConn.ExecContext(context.Background(), "RENAME TABLE reactions_rebuild_fail_test TO reactions")
		if err := rebuildStatsFromDB(context.Background()); err != nil {
			t.Errorf("rebuild after restoring reactions: %v", err)
		}
	})

	if err := rebuildStatsFromDB(ctx); err == nil {
		t.Fatal("expected rebuild to fail while reactions table is renamed")
	}
	if got := statsPaymentTotalTips.Load(); got != 0 {
		t.Fatalf("statsPaymentTotalTips after failed rebuild: got %d want 0 (stale was %d)", got, stalePaymentTotal)
	}
}
