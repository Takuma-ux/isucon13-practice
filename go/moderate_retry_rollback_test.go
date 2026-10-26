package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// After a failed txn (rolled back), retry must not leave an extra ng_words row from the aborted attempt.
func TestModerateDeadlockRetryRollsBackNGWord(t *testing.T) {
	if os.Getenv("ISUCON13_MODERATE_CONVERGE_TEST") != "1" {
		t.Skip("set ISUCON13_MODERATE_CONVERGE_TEST=1")
	}
	db := testDB(t)
	dbConn = db
	ctx := context.Background()
	const ls = 999
	word := fmt.Sprintf("retryrb%d", time.Now().UnixNano())

	var before int
	if err := db.GetContext(ctx, &before, `SELECT COUNT(*) FROM ng_words WHERE livestream_id = ? AND word = ?`, ls, word); err != nil {
		t.Fatalf("before: %v", err)
	}

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO ng_words(user_id, livestream_id, word, created_at) VALUES (1, ?, ?, ?)`,
		ls, word, time.Now().Unix())
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var mid int
	if err := db.GetContext(ctx, &mid, `SELECT COUNT(*) FROM ng_words WHERE livestream_id = ? AND word = ?`, ls, word); err != nil {
		t.Fatalf("mid: %v", err)
	}
	if mid != before {
		t.Fatalf("rolled-back ng_word visible: before=%d mid=%d", before, mid)
	}

	wordID, _, err := moderateLivestream(ctx, 1, ls, word)
	if err != nil {
		t.Fatalf("moderate: %v", err)
	}
	if wordID == 0 {
		t.Fatal("wordID 0")
	}

	var after int
	if err := db.GetContext(ctx, &after, `SELECT COUNT(*) FROM ng_words WHERE livestream_id = ? AND word = ?`, ls, word); err != nil {
		t.Fatalf("after: %v", err)
	}
	if after != before+1 {
		t.Fatalf("expected exactly one new ng_word row, before=%d after=%d", before, after)
	}
}
