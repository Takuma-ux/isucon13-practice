// Empirical convergence test for deleteLivecommentsHittingNgWords (requires local DB).
package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func testDB(t *testing.T) *sqlx.DB {
	if os.Getenv("ISUCON13_MODERATE_CONVERGE_TEST") != "1" {
		t.Skip("set ISUCON13_MODERATE_CONVERGE_TEST=1")
	}
	db, err := connectDB(nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return db
}

// TestSpamDeleteLoopsUntilEmpty confirms the for { ... len(ids)==0 return } loop.
func TestSpamDeleteLoopsUntilEmpty(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	const ls = 999
	word := fmt.Sprintf("loopchk%d", time.Now().UnixNano())

	for i := 0; i < 1200; i++ {
		_, err := db.ExecContext(ctx,
			`INSERT INTO livecomments (user_id, livestream_id, comment, tip, created_at) VALUES (1, ?, ?, 0, ?)`,
			ls, fmt.Sprintf("x-%s-%d", word, i), time.Now().Unix())
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO ng_words(user_id, livestream_id, word, created_at) VALUES (1, ?, ?, ?)`,
		ls, word, time.Now().Unix())
	if err != nil {
		t.Fatalf("ng_word: %v", err)
	}

	if _, err := deleteLivecommentsHittingNgWords(ctx, tx, ls); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var left int
	if err := tx.GetContext(ctx, &left,
		`SELECT COUNT(*) FROM livecomments lc WHERE lc.livestream_id = ? AND lc.comment LIKE ?`,
		ls, "%"+word+"%"); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 0 {
		t.Fatalf("expected 0 matching comments in tx, got %d", left)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestSpamDeleteSecondBatchCatchesRemainder: 600 seeded rows => batch1=500, production loop finishes rest.
func TestSpamDeleteSecondBatchCatchesRemainder(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	const ls = 999
	word := fmt.Sprintf("concur%d", time.Now().UnixNano())

	for i := 0; i < 600; i++ {
		_, err := db.ExecContext(ctx,
			`INSERT INTO livecomments (user_id, livestream_id, comment, tip, created_at) VALUES (1, ?, ?, 0, ?)`,
			ls, fmt.Sprintf("y-%s-%d", word, i), time.Now().Unix())
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO ng_words(user_id, livestream_id, word, created_at) VALUES (1, ?, ?, ?)`,
		ls, word, time.Now().Unix())
	if err != nil {
		t.Fatalf("ng_word: %v", err)
	}

	const selectIDs = `
		SELECT lc.id FROM livecomments lc
		WHERE lc.livestream_id = ?
		AND EXISTS (
			SELECT 1 FROM ng_words nw
			WHERE nw.livestream_id = ?
			AND lc.comment LIKE CONCAT('%', nw.word, '%')
		)
		ORDER BY lc.id
		LIMIT ?
		FOR UPDATE`
	var ids []int64
	if err := tx.SelectContext(ctx, &ids, selectIDs, ls, ls, spamLivecommentDeleteBatchSize); err != nil {
		t.Fatalf("select1: %v", err)
	}
	if len(ids) != 500 {
		t.Fatalf("expected first batch 500 ids, got %d", len(ids))
	}
	query, args, err := sqlx.In("DELETE FROM livecomments WHERE id IN (?)", ids)
	if err != nil {
		t.Fatalf("in: %v", err)
	}
	query = tx.Rebind(query)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("del1: %v", err)
	}

	if _, err := deleteLivecommentsHittingNgWords(ctx, tx, ls); err != nil {
		t.Fatalf("delete rest: %v", err)
	}

	var left int
	if err := tx.GetContext(ctx, &left,
		`SELECT COUNT(*) FROM livecomments WHERE livestream_id = ? AND comment LIKE ?`,
		ls, "%"+word+"%"); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 0 {
		t.Fatalf("second batch did not converge: %d rows still match", left)
	}
}

// Late insert while moderate txn is open: blocked until commit, then survives delete phase.
func TestSpamDeleteLateInsertAfterDeletePhaseWaitsOnLocks(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	const ls = 999
	word := fmt.Sprintf("lateopen%d", time.Now().UnixNano())
	lateComment := fmt.Sprintf("payload-%s-end", word)

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO ng_words(user_id, livestream_id, word, created_at) VALUES (1, ?, ?, ?)`,
		ls, word, time.Now().Unix())
	if err != nil {
		t.Fatalf("ng_word: %v", err)
	}
	if _, err := deleteLivecommentsHittingNgWords(ctx, tx, ls); err != nil {
		t.Fatalf("delete: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(ctx,
			`INSERT INTO livecomments (user_id, livestream_id, comment, tip, created_at) VALUES (1, ?, ?, 0, ?)`,
			ls, lateComment, time.Now().Unix())
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("insert completed before moderate commit (unexpected): %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("late insert after commit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("insert did not complete after moderate commit")
	}

	var n int
	if err := db.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM livecomments WHERE livestream_id = ? AND comment = ?`, ls, lateComment); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected late insert to survive moderate txn after delete phase, count=%d", n)
	}
}
