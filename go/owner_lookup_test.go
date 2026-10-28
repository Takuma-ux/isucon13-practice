package main

import (
	"context"
	"testing"
)

func TestOwnerUserIDForLivestreamStatsFastPath(t *testing.T) {
	const livestreamID int64 = 9_000_001
	const ownerID int64 = 9_000_002

	statsRebuildMu.Lock()
	saved := snapshotStatsMaps()
	statsLiveOwners[livestreamID] = ownerID
	statsRebuildMu.Unlock()
	t.Cleanup(func() {
		statsRebuildMu.Lock()
		restoreStatsMaps(saved)
		statsRebuildMu.Unlock()
	})

	got, err := ownerUserIDForLivestream(context.Background(), nil, livestreamID)
	if err != nil {
		t.Fatalf("ownerUserIDForLivestream: %v", err)
	}
	if got != ownerID {
		t.Fatalf("got owner %d want %d", got, ownerID)
	}
}
