package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// prependFakePdnsutilToPATH installs a no-op pdnsutil for sql/init.sh (see capture-init-cold-warm-profile.sh).
func prependFakePdnsutilToPATH(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "pdnsutil")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake pdnsutil: %v", err)
	}
	prev := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+prev)
	t.Setenv("ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS", "127.0.0.1")
}

func statsCounterTestDB(t *testing.T) {
	if os.Getenv("ISUCON13_STATS_COUNTER_TEST") != "1" {
		t.Skip("set ISUCON13_STATS_COUNTER_TEST=1 and run with local MySQL")
	}
	requireSharedIntegrationDB(t)
	ctx := context.Background()
	if err := rebuildStatsFromDB(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
}

func TestRebuildStatsMatchesSQLGroundTruth(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()
	if err := rebuildStatsFromDB(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	var username string
	if err := tx.GetContext(ctx, &username, "SELECT name FROM users ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("user: %v", err)
	}
	var livestreamID int64
	if err := tx.GetContext(ctx, &livestreamID, "SELECT id FROM livestreams ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("livestream: %v", err)
	}

	sqlUser, err := userStatisticsFromSQL(ctx, tx, username)
	if err != nil {
		t.Fatalf("sql user stats: %v", err)
	}
	memUser, err := userStatisticsFromMemory(username)
	if err != nil {
		t.Fatalf("mem user stats: %v", err)
	}
	if !reflect.DeepEqual(sqlUser, memUser) {
		t.Fatalf("user stats mismatch\nsql: %+v\nmem: %+v", sqlUser, memUser)
	}

	sqlLS, err := livestreamStatisticsFromSQL(ctx, tx, livestreamID)
	if err != nil {
		t.Fatalf("sql livestream stats: %v", err)
	}
	memLS, err := livestreamStatisticsFromMemory(livestreamID)
	if err != nil {
		t.Fatalf("mem livestream stats: %v", err)
	}
	if !reflect.DeepEqual(sqlLS, memLS) {
		t.Fatalf("livestream stats mismatch\nsql: %+v\nmem: %+v", sqlLS, memLS)
	}
}

func TestStatsHandlersIssueZeroSQLQueries(t *testing.T) {
	statsCounterTestDB(t)
	var livestreamID int64
	if err := dbConn.GetContext(context.Background(), &livestreamID, "SELECT id FROM livestreams ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("livestream: %v", err)
	}

	e2 := newProcessCacheTestEcho()
	e2.GET("/api/user/:username/statistics", getUserStatisticsHandler)
	e2.GET("/api/livestream/:livestream_id/statistics", getLivestreamStatisticsHandler)
	cookie := loginTestUser(t, e2, "test001", "test")

	start, err := mysqlSessionQuestions(dbConn)
	if err != nil {
		t.Fatalf("questions start: %v", err)
	}

	code, _ := getJSON(t, e2, http.MethodGet, "/api/user/test001/statistics", cookie)
	if code != http.StatusOK {
		t.Fatalf("user stats status %d", code)
	}
	code, _ = getJSON(t, e2, http.MethodGet, "/api/livestream/"+itoa64(livestreamID)+"/statistics", cookie)
	if code != http.StatusOK {
		t.Fatalf("livestream stats status %d", code)
	}

	end, err := mysqlSessionQuestions(dbConn)
	if err != nil {
		t.Fatalf("questions end: %v", err)
	}
	delta := end - start
	// One extra Questions increment comes from the final SHOW SESSION STATUS measurement itself.
	if delta > 1 {
		t.Fatalf("expected no SQL on stats read path (delta<=1), Questions delta=%d", delta)
	}
}

func itoa64(n int64) string {
	return strconvFormatInt(n)
}

func strconvFormatInt(n int64) string {
	// small helper to avoid importing strconv in test-only path
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestConcurrentStatsIncrementsRace(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()
	const livestreamID int64 = 1
	ownerID := statsLiveOwners[livestreamID]
	if ownerID == 0 {
		t.Fatal("missing owner")
	}
	if err := rebuildStatsFromDB(ctx); err != nil {
		t.Fatal(err)
	}
	ls := statsLivestreams[livestreamID]
	if ls == nil {
		t.Fatal("missing livestream stats")
	}
	baseReactions := ls.reactions.Load()
	baseComments := ls.comments.Load()
	baseTips := ls.tips.Load()

	const workers = 32
	const each = 50
	var wg sync.WaitGroup
	wg.Add(workers * 3)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				statsOnReactionPosted(ownerID, livestreamID, "👍")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				statsOnLivecommentPosted(ownerID, livestreamID, 1)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				statsOnEnter(livestreamID)
				statsOnExit(livestreamID, 1)
			}
		}()
	}
	wg.Wait()

	wantReactions := baseReactions + int64(workers*each)
	wantComments := baseComments + int64(workers*each)
	wantTips := baseTips + int64(workers*each)
	if ls.reactions.Load() != wantReactions {
		t.Fatalf("reactions: got %d want %d", ls.reactions.Load(), wantReactions)
	}
	if ls.comments.Load() != wantComments {
		t.Fatalf("comments: got %d want %d", ls.comments.Load(), wantComments)
	}
	if ls.tips.Load() != wantTips {
		t.Fatalf("tips: got %d want %d", ls.tips.Load(), wantTips)
	}
}

func TestMaxTipModerateVsPostRace(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()
	const lsID int64 = 1
	ownerID := statsLiveOwners[lsID]
	if ownerID == 0 {
		t.Fatal("missing livestream owner in stats maps")
	}
	_, err := dbConn.ExecContext(ctx, "DELETE FROM livecomments WHERE livestream_id = ?", lsID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dbConn.ExecContext(ctx, `
		INSERT INTO livecomments (user_id, livestream_id, comment, tip, created_at)
		VALUES (?, ?, 'seed', 10, UNIX_TIMESTAMP())`, ownerID, lsID)
	if err != nil {
		t.Fatal(err)
	}
	if err := rebuildStatsFromDB(ctx); err != nil {
		t.Fatal(err)
	}
	ls := statsLivestreams[lsID]
	if ls == nil {
		t.Fatal("missing livestream stats")
	}
	// Stale in-memory max as if a since-deleted comment had tip 100.
	ls.maxTip = 100

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		time.Sleep(2 * time.Millisecond)
		// Simulate moderate deleting max holder: recompute path runs MAX against DB.
		statsOnModerateDeleted(ctx, lsID, ownerID, moderateDeleteStats{Count: 1, TipSum: 100, MaxDeleted: 100})
	}()
	go func() {
		defer wg.Done()
		statsOnLivecommentPosted(ownerID, lsID, 200)
	}()
	wg.Wait()

	mu := livestreamMaxTipLock(lsID)
	mu.Lock()
	maxTip := ls.maxTip
	mu.Unlock()
	if maxTip != 200 {
		t.Fatalf("max_tip race: got %d want 200 (mutex must not allow stale recompute to win)", maxTip)
	}
}

func TestInitializeResetsStatsCountersHTTP(t *testing.T) {
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

	// Corrupt in-memory counters (must not match DB).
	statsOnReactionPosted(1, 1, "🔥")
	statsOnReactionPosted(1, 1, "🔥")
	statsOnLivecommentPosted(1, 1, 9999)
	if u := statsUsers[1]; u != nil && u.reactions.Load() < 2 {
		t.Fatalf("expected corrupted reaction count, got %d", u.reactions.Load())
	}

	prependFakePdnsutilToPATH(t)

	e := echo.New()
	e.POST("/api/initialize", initializeHandler)
	req := httptest.NewRequest(http.MethodPost, "/api/initialize", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/initialize status %d body=%s", rec.Code, rec.Body.String())
	}

	// Reseed changes row ids; resolve streamer after initialize for stats/tag checks.
	if err := dbConn.GetContext(ctx, &username, "SELECT name FROM users ORDER BY id LIMIT 1"); err != nil {
		t.Fatal(err)
	}
	if err := dbConn.GetContext(ctx, &livestreamID, "SELECT id FROM livestreams ORDER BY id LIMIT 1"); err != nil {
		t.Fatal(err)
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	sqlUser, err := userStatisticsFromSQL(ctx, tx, username)
	if err != nil {
		t.Fatal(err)
	}
	memUser, err := userStatisticsFromMemory(username)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sqlUser, memUser) {
		t.Fatalf("after HTTP initialize user stats mismatch\nsql=%+v\nmem=%+v", sqlUser, memUser)
	}

	sqlLS, err := livestreamStatisticsFromSQL(ctx, tx, livestreamID)
	if err != nil {
		t.Fatal(err)
	}
	memLS, err := livestreamStatisticsFromMemory(livestreamID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sqlLS, memLS) {
		t.Fatalf("after HTTP initialize livestream stats mismatch\nsql=%+v\nmem=%+v", sqlLS, memLS)
	}

	// Tags are prewarmed synchronously in initializeHandler; GET /api/tag should still succeed.
	e.GET("/api/tag", getTagHandler)
	recTag := httptest.NewRecorder()
	e.ServeHTTP(recTag, httptest.NewRequest(http.MethodGet, "/api/tag", nil))
	if recTag.Code != http.StatusOK {
		t.Fatalf("GET /api/tag after initialize status %d body=%s", recTag.Code, recTag.Body.String())
	}
	var tagsBody TagsResponse
	if err := json.Unmarshal(recTag.Body.Bytes(), &tagsBody); err != nil {
		t.Fatal(err)
	}
	if len(tagsBody.Tags) == 0 {
		t.Fatal("expected non-empty tags after initialize")
	}
}

func TestStatsJSONEquivalenceMemoryVsSQL(t *testing.T) {
	statsCounterTestDB(t)
	ctx := context.Background()
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	var username string
	var livestreamID int64
	if err := tx.GetContext(ctx, &username, "SELECT name FROM users ORDER BY id LIMIT 1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.GetContext(ctx, &livestreamID, "SELECT id FROM livestreams ORDER BY id LIMIT 1"); err != nil {
		t.Fatal(err)
	}

	sqlUser, err := userStatisticsFromSQL(ctx, tx, username)
	if err != nil {
		t.Fatal(err)
	}
	memUser, err := userStatisticsFromMemory(username)
	if err != nil {
		t.Fatal(err)
	}
	stdUserSQL, _ := json.Marshal(sqlUser)
	stdUserMem, _ := json.Marshal(memUser)
	if !bytes.Equal(stdUserSQL, stdUserMem) {
		t.Fatalf("user JSON bytes differ\nsql=%s\nmem=%s", stdUserSQL, stdUserMem)
	}

	sqlLS, err := livestreamStatisticsFromSQL(ctx, tx, livestreamID)
	if err != nil {
		t.Fatal(err)
	}
	memLS, err := livestreamStatisticsFromMemory(livestreamID)
	if err != nil {
		t.Fatal(err)
	}
	lsSQL, _ := json.Marshal(sqlLS)
	lsMem, _ := json.Marshal(memLS)
	if !bytes.Equal(lsSQL, lsMem) {
		t.Fatalf("livestream JSON bytes differ\nsql=%s\nmem=%s", lsSQL, lsMem)
	}
}
