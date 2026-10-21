package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestWithMySQLDeadlockRetryDoesNotRetrySpam(t *testing.T) {
	var calls atomic.Int32
	err := withMySQLDeadlockRetry(func() error {
		calls.Add(1)
		return errPostLivecommentSpam
	})
	if !errors.Is(err, errPostLivecommentSpam) {
		t.Fatalf("expected spam sentinel, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("spam path must not retry: calls=%d", calls.Load())
	}
}

func TestWithMySQLDeadlockRetryRetries1213Only(t *testing.T) {
	var calls atomic.Int32
	deadlock := &mysql.MySQLError{Number: 1213, Message: "Deadlock"}
	err := withMySQLDeadlockRetry(func() error {
		calls.Add(1)
		if calls.Load() < 3 {
			return deadlock
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
}

// Concurrent moderate DELETE + postLivecomment INSERT on the same stream (local MySQL).
func TestPostLivecommentDeadlockRetryUnderModerateContention(t *testing.T) {
	if os.Getenv("ISUCON13_MODERATE_CONVERGE_TEST") != "1" {
		t.Skip("set ISUCON13_MODERATE_CONVERGE_TEST=1")
	}
	db := testDB(t)
	dbConn = db
	ctx := context.Background()
	const ls = 999
	const streamOwner = int64(1)

	var commenterID int64
	if err := db.GetContext(ctx, &commenterID, `SELECT id FROM users ORDER BY id LIMIT 1`); err != nil {
		t.Fatalf("commenter: %v", err)
	}

	prefix := fmt.Sprintf("dlcmt%d", time.Now().UnixNano())
	for i := 0; i < 400; i++ {
		_, err := db.ExecContext(ctx,
			`INSERT INTO livecomments (user_id, livestream_id, comment, tip, created_at) VALUES (?, ?, ?, 0, ?)`,
			streamOwner, ls, fmt.Sprintf("seed-%s-%d", prefix, i), time.Now().Unix())
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	var moderateOK, postOK, postFail atomic.Int64
	deadline := time.Now().Add(4 * time.Second)
	var wg sync.WaitGroup

	for m := 0; m < 6; m++ {
		wg.Add(1)
		go func(mid int) {
			defer wg.Done()
			for time.Now().Before(deadline) {
				word := fmt.Sprintf("%s-m%d", prefix, mid)
				_, err := moderateLivestream(ctx, streamOwner, ls, word)
				if err == nil {
					moderateOK.Add(1)
				}
				_, _ = db.ExecContext(ctx,
					`INSERT INTO livecomments (user_id, livestream_id, comment, tip, created_at) VALUES (?, ?, ?, 0, ?)`,
					streamOwner, ls, fmt.Sprintf("hit-%s", word), time.Now().Unix())
			}
		}(m)
	}

	for p := 0; p < 12; p++ {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			n := 0
			for time.Now().Before(deadline) {
				req := &PostLivecommentRequest{
					Comment: fmt.Sprintf("clean-%s-%d-%d", prefix, pid, n),
					Tip:     0,
				}
				_, err := postLivecommentWithDeadlockRetry(ctx, commenterID, ls, req)
				n++
				if err != nil {
					if errors.Is(err, errPostLivecommentSpam) {
						continue
					}
					postFail.Add(1)
					continue
				}
				postOK.Add(1)
			}
		}(p)
	}

	wg.Wait()
	t.Logf("moderateOK=%d postOK=%d postFail=%d", moderateOK.Load(), postOK.Load(), postFail.Load())
	if postOK.Load() == 0 {
		t.Fatal("expected successful posts under contention")
	}
	if postFail.Load() > 0 {
		t.Fatalf("deadlock retry should prevent viewer-facing failures; postFail=%d", postFail.Load())
	}
}

func TestPostLivecommentSpamRejectionNoDBInsert(t *testing.T) {
	if os.Getenv("ISUCON13_MODERATE_CONVERGE_TEST") != "1" {
		t.Skip("set ISUCON13_MODERATE_CONVERGE_TEST=1")
	}
	db := testDB(t)
	dbConn = db
	ctx := context.Background()
	const ls = 999
	word := fmt.Sprintf("spamonly%d", time.Now().UnixNano())

	_, err := db.ExecContext(ctx,
		`INSERT INTO ng_words(user_id, livestream_id, word, created_at) VALUES (1, ?, ?, ?)`,
		ls, word, time.Now().Unix())
	if err != nil {
		t.Fatalf("ng_word: %v", err)
	}

	var commenterID int64
	if err := db.GetContext(ctx, &commenterID, `SELECT id FROM users ORDER BY id LIMIT 1`); err != nil {
		t.Fatalf("commenter: %v", err)
	}

	var before int
	if err := db.GetContext(ctx, &before,
		`SELECT COUNT(*) FROM livecomments WHERE livestream_id = ? AND comment LIKE ?`, ls, "%"+word+"%"); err != nil {
		t.Fatalf("before: %v", err)
	}

	_, err = postLivecommentWithDeadlockRetry(ctx, commenterID, ls, &PostLivecommentRequest{
		Comment: "contains " + word + " marker",
		Tip:     0,
	})
	if !errors.Is(err, errPostLivecommentSpam) {
		t.Fatalf("expected spam error, got %v", err)
	}

	var after int
	if err := db.GetContext(ctx, &after,
		`SELECT COUNT(*) FROM livecomments WHERE livestream_id = ? AND comment LIKE ?`, ls, "%"+word+"%"); err != nil {
		t.Fatalf("after: %v", err)
	}
	if after != before {
		t.Fatalf("spam rejection must not insert row: before=%d after=%d", before, after)
	}
}
