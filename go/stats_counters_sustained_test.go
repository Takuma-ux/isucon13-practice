package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
)

func syncMapLen(m *sync.Map) int {
	n := 0
	m.Range(func(_, _ interface{}) bool {
		n++
		return true
	})
	return n
}

func totalEmojiHistogramEntries() int {
	n := 0
	for _, u := range statsUsers {
		if u == nil {
			continue
		}
		u.emoji.mu.Lock()
		n += len(u.emoji.counts)
		u.emoji.mu.Unlock()
	}
	return n
}

func statsMapSnapshot() (users, livestreams, owners, maxTipLocks, emojiKeys int) {
	statsRebuildMu.RLock()
	users = len(statsUsers)
	livestreams = len(statsLivestreams)
	owners = len(statsLiveOwners)
	statsRebuildMu.RUnlock()
	maxTipLocks = syncMapLen(&livestreamMaxTipLocks)
	emojiKeys = totalEmojiHistogramEntries()
	return users, livestreams, owners, maxTipLocks, emojiKeys
}

func dbEntityCounts(t *testing.T, ctx context.Context) (userCnt, liveCnt int) {
	if err := dbConn.GetContext(ctx, &userCnt, "SELECT COUNT(*) FROM users"); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if err := dbConn.GetContext(ctx, &liveCnt, "SELECT COUNT(*) FROM livestreams"); err != nil {
		t.Fatalf("count livestreams: %v", err)
	}
	return userCnt, liveCnt
}

func assertStatsMatchSQLGroundTruth(t *testing.T, ctx context.Context, username string, livestreamID int64) {
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	sqlUser, err := userStatisticsFromSQL(ctx, tx, username)
	if err != nil {
		t.Fatalf("sql user: %v", err)
	}
	memUser, err := userStatisticsFromMemory(username)
	if err != nil {
		t.Fatalf("mem user: %v", err)
	}
	if !reflect.DeepEqual(sqlUser, memUser) {
		t.Fatalf("user stats mismatch\nsql=%+v\nmem=%+v", sqlUser, memUser)
	}

	sqlLS, err := livestreamStatisticsFromSQL(ctx, tx, livestreamID)
	if err != nil {
		t.Fatalf("sql livestream: %v", err)
	}
	memLS, err := livestreamStatisticsFromMemory(livestreamID)
	if err != nil {
		t.Fatalf("mem livestream: %v", err)
	}
	if !reflect.DeepEqual(sqlLS, memLS) {
		t.Fatalf("livestream stats mismatch\nsql=%+v\nmem=%+v", sqlLS, memLS)
	}
}

// Simulates many bench/pretest cycles in one process via rebuildStatsFromDB (same path as POST /api/initialize after init.sh).
func TestSustainedStatsRebuildCycles(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()

	var username string
	var livestreamID int64
	if err := dbConn.GetContext(ctx, &username, "SELECT name FROM users ORDER BY id LIMIT 1"); err != nil {
		t.Fatal(err)
	}
	if err := dbConn.GetContext(ctx, &livestreamID, "SELECT id FROM livestreams ORDER BY id LIMIT 1"); err != nil {
		t.Fatal(err)
	}
	wantUsers, wantLives := dbEntityCounts(t, ctx)

	if err := rebuildStatsFromDB(ctx); err != nil {
		t.Fatal(err)
	}
	baseUsers, baseLives, baseOwners, baseLocks, baseEmoji := statsMapSnapshot()
	if baseUsers != wantUsers || baseLives != wantLives || baseOwners != wantLives {
		t.Fatalf("baseline map sizes: users=%d live=%d owners=%d (want users=%d live=%d)",
			baseUsers, baseLives, baseOwners, wantUsers, wantLives)
	}
	if baseLocks != 0 {
		t.Fatalf("expected empty livestreamMaxTipLocks after rebuild, got %d", baseLocks)
	}

	runtime.GC()
	var memStart runtime.MemStats
	runtime.ReadMemStats(&memStart)
	goroStart := runtime.NumGoroutine()

	const cycles = 20
	for i := 0; i < cycles; i++ {
		// Poison maps with IDs that cannot exist after a full reseed/rebuild.
		statsRebuildMu.Lock()
		ghostUser := int64(9_000_000_000 + i)
		ghostLive := int64(8_000_000_000 + i)
		statsUsers[ghostUser] = &userStatCounters{emoji: userEmojiHistogram{counts: map[string]int64{"ghost": 1}}}
		statsLivestreams[ghostLive] = &livestreamStatCounters{maxTip: 42}
		statsLiveOwners[ghostLive] = ghostUser
		statsAllUserIDs = append(statsAllUserIDs, ghostUser)
		statsAllLiveIDs = append(statsAllLiveIDs, ghostLive)
		livestreamMaxTipLocks.Store(ghostLive, &sync.Mutex{})
		statsRebuildMu.Unlock()

		if err := rebuildStatsFromDB(ctx); err != nil {
			t.Fatalf("rebuild cycle %d: %v", i, err)
		}

		u, l, o, locks, emoji := statsMapSnapshot()
		if u != baseUsers || l != baseLives || o != baseOwners {
			t.Fatalf("cycle %d: map growth/stale sizes users=%d live=%d owners=%d (want %d/%d/%d)",
				i, u, l, o, baseUsers, baseLives, baseOwners)
		}
		if locks != 0 {
			t.Fatalf("cycle %d: livestreamMaxTipLocks not cleared, len=%d", i, locks)
		}
		if _, ok := statsUsers[ghostUser]; ok {
			t.Fatalf("cycle %d: stale ghost user %d survived rebuild", i, ghostUser)
		}
		if _, ok := statsLivestreams[ghostLive]; ok {
			t.Fatalf("cycle %d: stale ghost livestream %d survived rebuild", i, ghostLive)
		}
		_ = emoji // emoji keys tied to users map; checked via SQL on last cycle
	}

	runtime.GC()
	var memEnd runtime.MemStats
	runtime.ReadMemStats(&memEnd)
	goroEnd := runtime.NumGoroutine()

	t.Logf("after %d rebuild cycles: goroutines %d -> %d", cycles, goroStart, goroEnd)
	t.Logf("heap_alloc: %d KiB -> %d KiB (not a hard limit; maps replaced each cycle)",
		memStart.HeapAlloc/1024, memEnd.HeapAlloc/1024)
	t.Logf("emoji histogram keys after last rebuild: %d", baseEmoji)

	if goroEnd > goroStart+5 {
		t.Fatalf("goroutine leak suspicion: start=%d end=%d", goroStart, goroEnd)
	}

	assertStatsMatchSQLGroundTruth(t, ctx, username, livestreamID)
}

// Exercises full POST /api/initialize (init.sh + rebuildStatsFromDB) repeatedly in one process.
func TestSustainedStatsInitializeHTTPCycles(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()

	prependFakePdnsutilToPATH(t)
	e := echo.New()
	e.POST("/api/initialize", initializeHandler)

	postInitialize := func(cycleLabel string) {
		req := httptest.NewRequest(http.MethodPost, "/api/initialize", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: POST /api/initialize status %d body=%s", cycleLabel, rec.Code, rec.Body.String())
		}
	}

	// Prior integration tests may leave extra rows; bootstrap to init.sh fixture before measuring wants.
	postInitialize("bootstrap")
	wantUsers, wantLives := dbEntityCounts(t, ctx)

	var username string
	var livestreamID int64
	if err := dbConn.GetContext(ctx, &username, "SELECT name FROM users ORDER BY id LIMIT 1"); err != nil {
		t.Fatal(err)
	}
	if err := dbConn.GetContext(ctx, &livestreamID, "SELECT id FROM livestreams ORDER BY id LIMIT 1"); err != nil {
		t.Fatal(err)
	}

	const cycles = 10
	var baseUsers, baseLives int
	runtime.GC()
	goroStart := runtime.NumGoroutine()

	for i := 0; i < cycles; i++ {
		postInitialize(fmt.Sprintf("cycle %d", i))

		u, l, o, locks, _ := statsMapSnapshot()
		if i == 0 {
			baseUsers, baseLives = u, l
		}
		if u != wantUsers || l != wantLives || o != wantLives {
			t.Fatalf("cycle %d: map sizes users=%d live=%d owners=%d (want %d/%d)",
				i, u, l, o, wantUsers, wantLives)
		}
		if u != baseUsers || l != baseLives {
			t.Fatalf("cycle %d: map sizes changed vs cycle 0 (%d/%d vs %d/%d)", i, u, l, baseUsers, baseLives)
		}
		if locks != 0 {
			t.Fatalf("cycle %d: livestreamMaxTipLocks len=%d after initialize", i, locks)
		}
	}

	goroEnd := runtime.NumGoroutine()
	t.Logf("after %d HTTP initialize cycles: goroutines %d -> %d", cycles, goroStart, goroEnd)
	if goroEnd > goroStart+5 {
		t.Fatalf("goroutine leak suspicion: start=%d end=%d", goroStart, goroEnd)
	}

	assertStatsMatchSQLGroundTruth(t, ctx, username, livestreamID)
}
