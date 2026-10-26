package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

func processCacheTestDB(t *testing.T) *sqlx.DB {
	if os.Getenv("ISUCON13_PROCESS_CACHE_TEST") != "1" {
		t.Skip("set ISUCON13_PROCESS_CACHE_TEST=1 and run with local MySQL")
	}
	return requireSharedIntegrationDB(t)
}

func mysqlSessionQuestions(db *sqlx.DB) (int64, error) {
	var name string
	var val int64
	if err := db.QueryRow("SHOW SESSION STATUS LIKE 'Questions'").Scan(&name, &val); err != nil {
		return 0, err
	}
	return val, nil
}

func newProcessCacheTestEcho() *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = errorResponseHandler
	e.Use(session.Middleware(newSessionCookieStore(secret)))
	e.POST("/api/login", loginHandler)
	e.POST("/api/icon", postIconHandler)
	e.GET("/api/livestream/search", searchLivestreamsHandler)
	e.GET("/api/livestream/:livestream_id", getLivestreamHandler)
	e.GET("/api/livestream/:livestream_id/livecomment", getLivecommentsHandler)
	e.POST("/api/livestream/:livestream_id/livecomment", postLivecommentHandler)
	e.GET("/api/livestream/:livestream_id/reaction", getReactionsHandler)
	e.POST("/api/livestream/:livestream_id/reaction", postReactionHandler)
	e.POST("/api/livestream/:livestream_id/enter", enterLivestreamHandler)
	return e
}

func loginTestUser(t *testing.T, e *echo.Echo, username, password string) string {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: status %d body=%s", username, rec.Code, rec.Body.String())
	}
	resp := rec.Result()
	defer resp.Body.Close()
	if len(resp.Cookies()) == 0 {
		t.Fatal("login: no cookie")
	}
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range resp.Cookies() {
		req2.AddCookie(c)
	}
	return req2.Header.Get("Cookie")
}

func getJSON(t *testing.T, e *echo.Echo, method, path, cookie string) (int, []byte) {
	req := httptest.NewRequest(method, path, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// TestProcessCacheIconUploadRefreshesEmbeddedHash is the primary correctness case for userProfileCache invalidation.
func TestProcessCacheIconUploadRefreshesEmbeddedHash(t *testing.T) {
	db := processCacheTestDB(t)
	dbConn = db
	invalidateProcessReadCaches()
	ctx := context.Background()

	var lsID int64
	if err := db.GetContext(ctx, &lsID, "SELECT id FROM livestreams WHERE user_id = 1 ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("livestream for user 1: %v", err)
	}

	e := newProcessCacheTestEcho()
	cookie := loginTestUser(t, e, "test001", "test")

	code, body := getJSON(t, e, http.MethodGet, fmt.Sprintf("/api/livestream/%d", lsID), cookie)
	if code != http.StatusOK {
		t.Fatalf("get livestream before icon: %d %s", code, body)
	}
	var before Livestream
	if err := json.Unmarshal(body, &before); err != nil {
		t.Fatalf("decode livestream: %v", err)
	}

	// Warm process caches.
	code, body = getJSON(t, e, http.MethodGet, fmt.Sprintf("/api/livestream/%d", lsID), cookie)
	if code != http.StatusOK {
		t.Fatalf("get livestream warm: %d", code)
	}

	image := []byte(fmt.Sprintf("process-cache-icon-test-%d", time.Now().UnixNano()))
	wantHash := hashIconBytes(image)
	reqBody, _ := json.Marshal(&PostIconRequest{Image: image})
	req := httptest.NewRequest(http.MethodPost, "/api/icon", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("post icon: %d %s", rec.Code, rec.Body.String())
	}

	code, body = getJSON(t, e, http.MethodGet, fmt.Sprintf("/api/livestream/%d", lsID), cookie)
	if code != http.StatusOK {
		t.Fatalf("get livestream after icon: %d %s", code, body)
	}
	var after Livestream
	if err := json.Unmarshal(body, &after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if after.Owner == nil {
		t.Fatal("missing owner")
	}
	if after.Owner.IconHash != wantHash {
		t.Fatalf("stale icon_hash: got %q want %q (before was %q)", after.Owner.IconHash, wantHash, before.Owner.IconHash)
	}

	// Comment list embeds livestream owner on nested livestream; ensure same hash on a commenter's user object.
	code, body = getJSON(t, e, http.MethodGet, fmt.Sprintf("/api/livestream/%d/livecomment?limit=5", lsID), cookie)
	if code != http.StatusOK {
		t.Fatalf("get livecomments: %d %s", code, body)
	}
	var comments []Livecomment
	if err := json.Unmarshal(body, &comments); err != nil {
		t.Fatalf("decode comments: %v", err)
	}
	for _, lc := range comments {
		if lc.User != nil && lc.User.ID == 1 && lc.User.IconHash != wantHash {
			t.Fatalf("comment user icon_hash stale: %q want %q", lc.User.IconHash, wantHash)
		}
	}
}

// TestProcessCacheInitializeClearsStaleShell ensures initialize-style invalidation does not serve pre-reset shells.
func TestProcessCacheInitializeClearsStaleShell(t *testing.T) {
	db := processCacheTestDB(t)
	dbConn = db
	ctx := context.Background()
	invalidateProcessReadCaches()

	var lsID int64
	var title string
	if err := db.QueryRowContext(ctx, "SELECT id, title FROM livestreams ORDER BY id LIMIT 1").Scan(&lsID, &title); err != nil {
		t.Fatalf("pick livestream: %v", err)
	}

	putLivestreamShellCache(&LivestreamShell{
		ID:           lsID,
		OwnerUserID:  1,
		Title:        "STALE_SHELL_TITLE_SHOULD_NEVER_SERVE",
		Description:  "stale",
		PlaylistUrl:  "http://example.invalid/",
		ThumbnailUrl: "http://example.invalid/",
		Tags:         []Tag{},
		StartAt:      0,
		EndAt:        1,
	})
	putUserProfileCache(&User{ID: 1, Name: "stale-user", IconHash: defaultIconHash}, 0)

	invalidateProcessReadCaches()

	e := newProcessCacheTestEcho()
	cookie := loginTestUser(t, e, "test001", "test")
	code, body := getJSON(t, e, http.MethodGet, fmt.Sprintf("/api/livestream/%d", lsID), cookie)
	if code != http.StatusOK {
		t.Fatalf("get livestream: %d %s", code, body)
	}
	var ls Livestream
	if err := json.Unmarshal(body, &ls); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ls.Title == "STALE_SHELL_TITLE_SHOULD_NEVER_SERVE" {
		t.Fatal("served stale shell title after invalidateProcessReadCaches")
	}
	if ls.Title != title {
		t.Fatalf("title mismatch: got %q want %q from DB", ls.Title, title)
	}
	if ls.Owner != nil && ls.Owner.Name == "stale-user" {
		t.Fatal("served stale user profile after invalidateProcessReadCaches")
	}
}

// TestProcessCacheNewLivestreamMissPopulate confirms a brand-new row is not blocked by negative cache.
func TestProcessCacheNewLivestreamMissPopulate(t *testing.T) {
	db := processCacheTestDB(t)
	dbConn = db
	ctx := context.Background()
	invalidateProcessReadCaches()

	const ownerID = 1
	now := time.Now().Unix()
	res, err := db.ExecContext(ctx,
		`INSERT INTO livestreams (user_id, title, description, playlist_url, thumbnail_url, start_at, end_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ownerID, "process-cache-new-ls", "desc", "https://media.xiii.isucon.dev/x", "https://media.xiii.isucon.dev/x.webp", now, now+3600)
	if err != nil {
		t.Fatalf("insert livestream: %v", err)
	}
	lsID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	var tagIDVal int64
	if err := db.GetContext(ctx, &tagIDVal, "SELECT id FROM tags ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("tag: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO livestream_tags (livestream_id, tag_id) VALUES (?, ?)", lsID, tagIDVal); err != nil {
		t.Fatalf("insert livestream_tags: %v", err)
	}

	_, ok := getLivestreamShellFromProcessCache(lsID)
	if ok {
		t.Fatal("unexpected shell before first fetch")
	}

	e := newProcessCacheTestEcho()
	cookie := loginTestUser(t, e, "test001", "test")
	code, body := getJSON(t, e, http.MethodGet, fmt.Sprintf("/api/livestream/%d", lsID), cookie)
	if code != http.StatusOK {
		t.Fatalf("get new livestream: %d %s", code, body)
	}
	var ls Livestream
	if err := json.Unmarshal(body, &ls); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ls.Title != "process-cache-new-ls" {
		t.Fatalf("title: got %q", ls.Title)
	}
	if len(ls.Tags) != 1 {
		t.Fatalf("tags: got %d", len(ls.Tags))
	}
	shell, ok := getLivestreamShellFromProcessCache(lsID)
	if !ok || shell.Title != "process-cache-new-ls" {
		t.Fatal("shell not populated after first fetch")
	}
}

func TestProcessCacheJourneySQLReduction(t *testing.T) {
	db := processCacheTestDB(t)
	dbConn = db
	invalidateProcessReadCaches()
	_ = warmTagsCache(context.Background())

	e := newProcessCacheTestEcho()
	cookie := loginTestUser(t, e, "test001", "test")

	var lsID int64
	if err := db.Get(&lsID, "SELECT id FROM livestreams ORDER BY id DESC LIMIT 1"); err != nil {
		t.Fatalf("livestream id: %v", err)
	}

	runJourney := func() int64 {
		start, err := mysqlSessionQuestions(db)
		if err != nil {
			t.Fatalf("questions start: %v", err)
		}
		if code, _ := getJSON(t, e, http.MethodGet, "/api/livestream/search?limit=50", ""); code != http.StatusOK {
			t.Fatalf("search: %d", code)
		}
		if code, _ := getJSON(t, e, http.MethodGet, fmt.Sprintf("/api/livestream/%d", lsID), cookie); code != http.StatusOK {
			t.Fatalf("get ls: %d", code)
		}
		if code, _ := getJSON(t, e, http.MethodPost, fmt.Sprintf("/api/livestream/%d/enter", lsID), cookie); code != http.StatusOK {
			t.Fatalf("enter: %d", code)
		}
		if code, _ := getJSON(t, e, http.MethodGet, fmt.Sprintf("/api/livestream/%d/livecomment?limit=20", lsID), cookie); code != http.StatusOK {
			t.Fatalf("comments: %d", code)
		}
		postBody, _ := json.Marshal(&PostLivecommentRequest{Comment: "cache-journey", Tip: 0})
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/livestream/%d/livecomment", lsID), bytes.NewReader(postBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", cookie)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("post comment: %d %s", rec.Code, rec.Body.String())
		}
		if code, _ := getJSON(t, e, http.MethodGet, fmt.Sprintf("/api/livestream/%d/reaction?limit=20", lsID), cookie); code != http.StatusOK {
			t.Fatalf("reactions: %d", code)
		}
		postRx, _ := json.Marshal(&PostReactionRequest{EmojiName: ":tada:"})
		req2 := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/livestream/%d/reaction", lsID), bytes.NewReader(postRx))
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("Cookie", cookie)
		rec2 := httptest.NewRecorder()
		e.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusCreated {
			t.Fatalf("post reaction: %d %s", rec2.Code, rec2.Body.String())
		}
		end, err := mysqlSessionQuestions(db)
		if err != nil {
			t.Fatalf("questions end: %v", err)
		}
		return end - start
	}

	invalidateProcessReadCaches()
	cold := runJourney()
	warm := runJourney()
	t.Logf("journey MySQL Questions delta (SESSION): cold=%d warm=%d — note: counter is server-side, not identical to prior ~58 app statement estimate", cold, warm)
	if warm >= cold {
		t.Fatalf("expected warm journey to use fewer SQL round-trips than cold: cold=%d warm=%d", cold, warm)
	}

	// Targeted: same livestream GET five times; later passes should avoid shell/owner SELECTs.
	invalidateProcessReadCaches()
	_ = warmTagsCache(context.Background())
	path := fmt.Sprintf("/api/livestream/%d", lsID)
	firstStart, _ := mysqlSessionQuestions(db)
	code, _ := getJSON(t, e, http.MethodGet, path, cookie)
	if code != http.StatusOK {
		t.Fatalf("first get: %d", code)
	}
	mid, _ := mysqlSessionQuestions(db)
	for i := 0; i < 4; i++ {
		if code, _ := getJSON(t, e, http.MethodGet, path, cookie); code != http.StatusOK {
			t.Fatalf("repeat get: %d", code)
		}
	}
	end, _ := mysqlSessionQuestions(db)
	firstPass := mid - firstStart
	repeatPass := end - mid
	t.Logf("five GET /api/livestream/:id — Questions after 1st=%d after next 4=%d (first includes fill SQL; repeats skip shell/owner/tag SELECTs)", firstPass, repeatPass)
	if repeatPass >= firstPass*2 {
		t.Fatalf("repeat fetches should cost less than 4× first-fetch fill: first=%d repeat4=%d", firstPass, repeatPass)
	}
}

func TestProcessCacheConcurrentReaders(t *testing.T) {
	db := processCacheTestDB(t)
	dbConn = db
	invalidateProcessReadCaches()
	ctx := context.Background()

	var lsID int64
	if err := db.GetContext(ctx, &lsID, "SELECT id FROM livestreams ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("ls: %v", err)
	}
	e := newProcessCacheTestEcho()
	cookie := loginTestUser(t, e, "test001", "test")
	code, _ := getJSON(t, e, http.MethodGet, fmt.Sprintf("/api/livestream/%d", lsID), cookie)
	if code != http.StatusOK {
		t.Fatalf("warm: %d", code)
	}

	const workers = 40
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, body := getJSON(t, e, http.MethodGet, fmt.Sprintf("/api/livestream/%d", lsID), cookie)
			if c != http.StatusOK {
				errCh <- fmt.Errorf("status %d %s", c, body)
				return
			}
			var ls Livestream
			if err := json.Unmarshal(body, &ls); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// Sanity: put/get shell and profile under write lock do not race (run with -race).
// TestProcessCacheStalePutAfterInvalidateRace simulates a slow fill that read OLD icon data from DB,
// then loses a race to POST /api/icon + invalidate before its putUserProfileCache runs.
// Without epoch-guarded puts, the delayed put would repopulate the cache with stale icon_hash.
func TestProcessCacheStalePutAfterInvalidateRace(t *testing.T) {
	db := processCacheTestDB(t)
	dbConn = db
	ctx := context.Background()
	const userID = 1
	invalidateProcessReadCaches()

	oldImage := []byte(fmt.Sprintf("race-old-icon-%d", time.Now().UnixNano()))
	oldHash := hashIconBytes(oldImage)
	if _, err := postUserIcon(ctx, userID, oldImage); err != nil {
		t.Fatalf("seed old icon: %v", err)
	}
	putUserProfileCache(&User{ID: userID, Name: "test001", IconHash: oldHash}, snapshotUserProfileCacheEpoch(userID))

	fillEpoch := snapshotUserProfileCacheEpoch(userID)

	// Build stale User as if fillUsersByIDs had finished SELECTs before any upload.
	staleUser := User{
		ID:       userID,
		Name:     "test001",
		IconHash: oldHash,
	}

	atPut := make(chan struct{})
	releasePut := make(chan struct{})
	fillDone := make(chan struct{})

	userProfileCachePutDelayHook = func(uid int64) {
		if uid != userID {
			return
		}
		close(atPut)
		<-releasePut
	}
	defer func() { userProfileCachePutDelayHook = nil }()

	go func() {
		putUserProfileCacheWithEpoch(&staleUser, fillEpoch)
		close(fillDone)
	}()

	select {
	case <-atPut:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delayed put hook")
	}

	newImage := []byte(fmt.Sprintf("race-new-icon-%d", time.Now().UnixNano()))
	newHash := hashIconBytes(newImage)
	if _, err := postUserIcon(ctx, userID, newImage); err != nil {
		t.Fatalf("upload new icon during race: %v", err)
	}
	if newHash == oldHash {
		t.Fatal("new icon hash must differ from old")
	}
	// postUserIcon write-through refreshes cache with new hash (epoch bumped).

	close(releasePut)
	select {
	case <-fillDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stale put goroutine")
	}
	userProfileCachePutDelayHook = nil

	if cached, ok := getUserProfileFromProcessCache(userID); ok && cached.IconHash == oldHash {
		t.Fatalf("RACE CONFIRMED: cache holds stale icon_hash %q after invalidate+new upload (want %q)", oldHash, newHash)
	}

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	users, err := fillUsersByIDs(ctx, tx, []int64{userID})
	if err != nil {
		t.Fatalf("fillUsersByIDs: %v", err)
	}
	u := users[userID]
	if u == nil {
		t.Fatal("missing user from fill")
	}
	if u.IconHash != newHash {
		t.Fatalf("profile after race: got icon_hash %q want %q", u.IconHash, newHash)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cached, ok := getUserProfileFromProcessCache(userID)
	if !ok {
		t.Fatal("expected process cache populated with fresh profile after fill")
	}
	if cached.IconHash != newHash {
		t.Fatalf("cached icon_hash %q want %q", cached.IconHash, newHash)
	}
	t.Log("epoch-guarded putUserProfileCache dropped stale write after write-through; fresh hash served")
}

// TestProcessCacheStalePutAfterWriteThroughRace: stale fill put must lose to writeThroughUserProfileCache (epoch bump + fresh put).
func TestProcessCacheStalePutAfterWriteThroughRace(t *testing.T) {
	invalidateProcessReadCaches()
	const userID = 1

	oldHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	fillEpoch := snapshotUserProfileCacheEpoch(userID)

	staleUser := User{ID: userID, Name: "test001", IconHash: oldHash}
	freshUser := User{ID: userID, Name: "test001", IconHash: newHash}

	atPut := make(chan struct{})
	releasePut := make(chan struct{})
	fillDone := make(chan struct{})

	userProfileCachePutDelayHook = func(uid int64) {
		if uid != userID {
			return
		}
		close(atPut)
		<-releasePut
	}
	defer func() { userProfileCachePutDelayHook = nil }()

	go func() {
		putUserProfileCacheWithEpoch(&staleUser, fillEpoch)
		close(fillDone)
	}()

	select {
	case <-atPut:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delayed stale put")
	}

	writeThroughUserProfileCache(&freshUser)

	close(releasePut)
	select {
	case <-fillDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stale put goroutine")
	}

	cached, ok := getUserProfileFromProcessCache(userID)
	if !ok {
		t.Fatal("expected immediate cache hit after write-through")
	}
	if cached.IconHash != newHash {
		t.Fatalf("write-through poisoned by stale put: got %q want %q", cached.IconHash, newHash)
	}
	if cached.IconHash == oldHash {
		t.Fatalf("RACE CONFIRMED: stale icon_hash won over write-through")
	}
	t.Log("stale delayed put dropped; write-through value retained in cache")
}

// TestProcessCacheIconWriteThroughPopulatesCache verifies postUserIcon leaves a cache hit (not a cold miss).
func TestProcessCacheIconWriteThroughPopulatesCache(t *testing.T) {
	db := processCacheTestDB(t)
	dbConn = db
	ctx := context.Background()
	const userID = 1
	invalidateProcessReadCaches()

	image := []byte(fmt.Sprintf("write-through-icon-%d", time.Now().UnixNano()))
	wantHash := hashIconBytes(image)
	if _, err := postUserIcon(ctx, userID, image); err != nil {
		t.Fatalf("post icon: %v", err)
	}
	cached, ok := getUserProfileFromProcessCache(userID)
	if !ok {
		t.Fatal("expected cache populated after icon upload write-through")
	}
	if cached.IconHash != wantHash {
		t.Fatalf("cache icon_hash %q want %q", cached.IconHash, wantHash)
	}
}

// TestProcessCacheUnguardedPutWouldPoisonCache documents the pre-epoch failure mode (not production code path).
func TestProcessCacheUnguardedPutWouldPoisonCache(t *testing.T) {
	const userID = 99
	invalidateProcessReadCaches()
	oldHash := "0000000000000000000000000000000000000000000000000000000000000001"
	newHash := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

	invalidateUserProfileCache(userID)
	userProfileCacheMu.Lock()
	userProfileCache[userID] = cloneUser(&User{ID: userID, IconHash: oldHash})
	userProfileCacheMu.Unlock()

	invalidateUserProfileCache(userID)
	userProfileCacheMu.Lock()
	userProfileCache[userID] = cloneUser(&User{ID: userID, IconHash: oldHash})
	userProfileCacheMu.Unlock()

	u, ok := getUserProfileFromProcessCache(userID)
	if !ok || u.IconHash != oldHash {
		t.Fatalf("unguarded write after invalidate would serve stale hash in cache")
	}
	_ = newHash
	t.Log("without epoch guard, unconditional put after invalidate can repopulate stale icon_hash (production now rejects via fillEpoch)")
}

// TestProcessCacheLivestreamRowRepeatGET confirms getLivestreamHandler skips livestreams row SELECT on cache hit.
func TestProcessCacheLivestreamRowRepeatGET(t *testing.T) {
	db := processCacheTestDB(t)
	dbConn = db
	invalidateProcessReadCaches()
	ctx := context.Background()

	var lsID int64
	if err := db.GetContext(ctx, &lsID, "SELECT id FROM livestreams ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("livestream id: %v", err)
	}

	e := newProcessCacheTestEcho()
	cookie := loginTestUser(t, e, "test001", "test")
	path := fmt.Sprintf("/api/livestream/%d", lsID)

	code, _ := getJSON(t, e, http.MethodGet, path, cookie)
	if code != http.StatusOK {
		t.Fatalf("warm get: %d", code)
	}

	start, err := mysqlSessionQuestions(db)
	if err != nil {
		t.Fatalf("questions: %v", err)
	}
	code, _ = getJSON(t, e, http.MethodGet, path, cookie)
	if code != http.StatusOK {
		t.Fatalf("second get: %d", code)
	}
	end, err := mysqlSessionQuestions(db)
	if err != nil {
		t.Fatalf("questions end: %v", err)
	}
	delta := end - start
	t.Logf("second GET /api/livestream/:id Questions delta=%d (expect no livestreams row SELECT; typically 0-2 for txn overhead)", delta)
	if delta > 3 {
		t.Fatalf("expected near-zero SQL for cached row repeat get, got Questions delta=%d", delta)
	}
}

// TestProcessCacheLivestreamSingleflightDedup ensures concurrent cold misses coalesce to ~one row SELECT.
func TestProcessCacheLivestreamSingleflightDedup(t *testing.T) {
	db := processCacheTestDB(t)
	dbConn = db
	ctx := context.Background()
	invalidateProcessReadCaches()

	var lsID int64
	if err := db.GetContext(ctx, &lsID, "SELECT id FROM livestreams ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("livestream id: %v", err)
	}

	const workers = 32
	processCacheTestLivestreamRowDBLoads.Store(0)

	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := db.BeginTxx(ctx, nil)
			if err != nil {
				errCh <- err
				return
			}
			defer tx.Rollback()
			_, err = getLivestreamModelByID(ctx, tx, lsID)
			if err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	loads := processCacheTestLivestreamRowDBLoads.Load()
	t.Logf("concurrent cold getLivestreamModelByID x%d: coalesced DB row loads=%d (want ~1)", workers, loads)
	if loads > 2 {
		t.Fatalf("expected ~1 coalesced livestream row SELECT, got %d", loads)
	}
}

// TestProcessCacheSingleflightEpochDuringCoalescedUserLoad: write-through during coalesced fetch must win over stale put.
func TestProcessCacheSingleflightEpochDuringCoalescedUserLoad(t *testing.T) {
	db := processCacheTestDB(t)
	dbConn = db
	ctx := context.Background()
	const userID = 1
	invalidateProcessReadCaches()

	oldImage := []byte(fmt.Sprintf("sf-old-%d", time.Now().UnixNano()))
	oldHash := hashIconBytes(oldImage)
	if _, err := postUserIcon(ctx, userID, oldImage); err != nil {
		t.Fatalf("seed icon: %v", err)
	}
	invalidateProcessReadCaches() // cold cache; DB still has old icon until write-through below

	newHash := hashIconBytes([]byte("sf-new-icon-fixed-payload"))
	freshUser := User{ID: userID, Name: "test001", IconHash: newHash}

	atDelay := make(chan struct{})
	releaseDelay := make(chan struct{})
	userProfileCoalescedFetchDelayHook = func(uid int64) {
		if uid != userID {
			return
		}
		close(atDelay)
		<-releaseDelay
	}
	defer func() { userProfileCoalescedFetchDelayHook = nil }()

	const workers = 12
	var wg sync.WaitGroup
	results := make([]User, workers)
	errCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tx, err := db.BeginTxx(ctx, nil)
			if err != nil {
				errCh <- err
				return
			}
			defer tx.Rollback()
			u, err := coalescedLoadUserProfile(ctx, tx, userID)
			if err != nil {
				errCh <- err
				return
			}
			results[idx] = u
		}(i)
	}

	select {
	case <-atDelay:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for coalesced fetch delay hook")
	}

	writeThroughUserProfileCache(&freshUser)
	close(releaseDelay)

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	for i, u := range results {
		if u.IconHash == oldHash {
			t.Fatalf("worker %d got stale coalesced icon_hash %q after write-through", i, oldHash)
		}
		if u.IconHash != newHash {
			t.Fatalf("worker %d icon_hash %q want %q", i, u.IconHash, newHash)
		}
	}
	cached, ok := getUserProfileFromProcessCache(userID)
	if !ok || cached.IconHash != newHash {
		t.Fatalf("cache after coalesced race: ok=%v hash=%v", ok, cached)
	}
	t.Log("singleflight coalesced load returned write-through profile after stale put was epoch-rejected")
}

func TestProcessCacheMutexSmoke(t *testing.T) {
	invalidateProcessReadCaches()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(id int64) {
			defer wg.Done()
			putLivestreamShellCache(&LivestreamShell{ID: id, OwnerUserID: 1, Title: "t", Tags: []Tag{}})
			_, _ = getLivestreamShellFromProcessCache(id)
		}(int64(i))
		go func(id int64) {
			defer wg.Done()
			putUserProfileCache(&User{ID: id, Name: "u", IconHash: hex.EncodeToString(sha256.New().Sum(nil))}, 0)
			_, _ = getUserProfileFromProcessCache(id)
			invalidateUserProfileCache(id)
		}(int64(i))
	}
	wg.Wait()
}
