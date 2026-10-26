package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// reserveLivestreamWriteOnce is the write transaction in reserveLivestreamHandler (through commit, no post-commit fill).
func reserveLivestreamWriteOnce(ctx context.Context, userID int64, req *ReserveLivestreamRequest) error {
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	termStartAt := time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC)
	termEndAt := time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC)
	reserveStartAt := time.Unix(req.StartAt, 0)
	reserveEndAt := time.Unix(req.EndAt, 0)
	if (reserveStartAt.Equal(termEndAt) || reserveStartAt.After(termEndAt)) || (reserveEndAt.Equal(termStartAt) || reserveEndAt.Before(termStartAt)) {
		return errReserveSlotFull
	}

	var slots []*ReservationSlotModel
	if err := tx.SelectContext(ctx, &slots, "SELECT * FROM reservation_slots WHERE start_at >= ? AND end_at <= ? FOR UPDATE", req.StartAt, req.EndAt); err != nil {
		return err
	}
	for _, slot := range slots {
		if slot.Slot < 1 {
			return errReserveSlotFull
		}
	}

	livestreamModel := &LivestreamModel{
		UserID:       userID,
		Title:        req.Title,
		Description:  req.Description,
		PlaylistUrl:  req.PlaylistUrl,
		ThumbnailUrl: req.ThumbnailUrl,
		StartAt:      req.StartAt,
		EndAt:        req.EndAt,
	}

	if _, err := tx.ExecContext(ctx, "UPDATE reservation_slots SET slot = slot - 1 WHERE start_at >= ? AND end_at <= ?", req.StartAt, req.EndAt); err != nil {
		return err
	}

	rs, err := tx.NamedExecContext(ctx, "INSERT INTO livestreams (user_id, title, description, playlist_url, thumbnail_url, start_at, end_at) VALUES(:user_id, :title, :description, :playlist_url, :thumbnail_url, :start_at, :end_at)", livestreamModel)
	if err != nil {
		return err
	}

	livestreamID, err := rs.LastInsertId()
	if err != nil {
		return err
	}

	if err := insertLivestreamTags(ctx, tx, livestreamID, req.Tags); err != nil {
		return err
	}

	return tx.Commit()
}

var errReserveSlotFull = errors.New("reserve: slot full")

func classifyReserveWriteErr(err error) (deadlock, slotFull, other bool) {
	if err == nil {
		return false, false, false
	}
	if errors.Is(err, errReserveSlotFull) {
		return false, true, false
	}
	if isMySQLDeadlock(err) {
		return true, false, false
	}
	msg := err.Error()
	if strings.Contains(msg, "予約できません") {
		return false, true, false
	}
	return false, false, true
}

// Concurrent reserves on the same long cold window (many reservation_slots rows under one FOR UPDATE).
func TestReserveLivestreamWriteContentionDeadlocks(t *testing.T) {
	if os.Getenv("ISUCON13_MODERATE_CONVERGE_TEST") != "1" {
		t.Skip("set ISUCON13_MODERATE_CONVERGE_TEST=1")
	}
	db := testDB(t)
	dbConn = db
	ctx := context.Background()

	// Long overlap: locks a large span of reservation_slots (bench cold-long style).
	const startAt = int64(1700874000)
	const endAt = int64(1700917200) // 12h window spanning many slot rows

	_, err := db.ExecContext(ctx,
		`UPDATE reservation_slots SET slot = slot + 500 WHERE start_at >= ? AND end_at <= ?`,
		startAt, endAt)
	if err != nil {
		t.Fatalf("boost slots: %v", err)
	}

	var userIDs []int64
	if err := db.SelectContext(ctx, &userIDs, `SELECT id FROM users ORDER BY id LIMIT 80`); err != nil {
		t.Fatalf("users: %v", err)
	}

	var ok, deadlock, slotFull, other atomic.Int64
	deadline := time.Now().Add(4 * time.Second)
	var wg sync.WaitGroup

	for _, uid := range userIDs {
		wg.Add(1)
		go func(userID int64) {
			defer wg.Done()
			n := 0
			for time.Now().Before(deadline) {
				req := &ReserveLivestreamRequest{
					Tags:         []int64{1},
					Title:        fmt.Sprintf("contention-%d-%d", userID, n),
					Description:  "stress",
					PlaylistUrl:  "https://example.com/p.m3u8",
					ThumbnailUrl: "https://example.com/t.webp",
					StartAt:      startAt,
					EndAt:        endAt,
				}
				err := reserveLivestreamWriteOnce(ctx, userID, req)
				n++
				d, sf, o := classifyReserveWriteErr(err)
				if err == nil {
					ok.Add(1)
					continue
				}
				if d {
					deadlock.Add(1)
				} else if sf {
					slotFull.Add(1)
				} else if o {
					other.Add(1)
				}
			}
		}(uid)
	}

	wg.Wait()
	t.Logf("reserve write contention: ok=%d deadlock=%d slotFull=%d other=%d",
		ok.Load(), deadlock.Load(), slotFull.Load(), other.Load())
	if other.Load() > 0 {
		t.Logf("note: %d non-deadlock errors (inspect if unexpected)", other.Load())
	}
	if deadlock.Load() > 0 {
		t.Fatalf("observed %d MySQL deadlocks on reserve write path — retry wrapper warranted", deadlock.Load())
	}
	if ok.Load() == 0 {
		t.Fatal("expected at least some successful reserves during contention window")
	}
	t.Log("no MySQL 1213 deadlocks observed on reserve write path (same bar as enter/exit probe)")
}

// Optional: reserve + moderate on same stream increases cross-table lock overlap (closer to full bench).
func TestReserveWriteContentionWithModerateDeadlocks(t *testing.T) {
	if os.Getenv("ISUCON13_MODERATE_CONVERGE_TEST") != "1" {
		t.Skip("set ISUCON13_MODERATE_CONVERGE_TEST=1")
	}
	db := testDB(t)
	dbConn = db
	ctx := context.Background()

	const ls = 999
	const startAt = int64(1700874000)
	const endAt = int64(1700884800)

	_, err := db.ExecContext(ctx,
		`UPDATE reservation_slots SET slot = slot + 300 WHERE start_at >= ? AND end_at <= ?`,
		startAt, endAt)
	if err != nil {
		t.Fatalf("boost slots: %v", err)
	}

	var userIDs []int64
	if err := db.SelectContext(ctx, &userIDs, `SELECT id FROM users ORDER BY id LIMIT 60`); err != nil {
		t.Fatalf("users: %v", err)
	}

	var reserveDeadlock, reserveOK, modOK atomic.Int64
	deadline := time.Now().Add(3 * time.Second)
	var wg sync.WaitGroup

	for i, uid := range userIDs {
		wg.Add(1)
		go func(userID int64, seq int) {
			defer wg.Done()
			for time.Now().Before(deadline) {
				req := &ReserveLivestreamRequest{
					Tags: []int64{1}, Title: fmt.Sprintf("mix-%d", seq), Description: "d",
					PlaylistUrl: "https://example.com/p.m3u8", ThumbnailUrl: "https://example.com/t.webp",
					StartAt: startAt, EndAt: endAt,
				}
				if err := reserveLivestreamWriteOnce(ctx, userID, req); err != nil {
					if isMySQLDeadlock(err) {
						reserveDeadlock.Add(1)
					}
				} else {
					reserveOK.Add(1)
				}
			}
		}(uid, i)
	}

	for m := 0; m < 6; m++ {
		wg.Add(1)
		go func(mid int) {
			defer wg.Done()
			for time.Now().Before(deadline) {
				word := fmt.Sprintf("resmix%d-%d", mid, time.Now().UnixNano())
				_, _, err := moderateLivestream(ctx, 1, ls, word)
				if err == nil {
					modOK.Add(1)
				}
				_, _ = db.ExecContext(ctx,
					`INSERT INTO livecomments (user_id, livestream_id, comment, tip, created_at) VALUES (?,?,?,?,?)`,
					1, ls, "hit-"+word, 0, time.Now().Unix())
			}
		}(m)
	}

	wg.Wait()
	t.Logf("reserve+moderate: reserveOK=%d reserveDeadlock=%d moderateOK=%d",
		reserveOK.Load(), reserveDeadlock.Load(), modOK.Load())
	if reserveDeadlock.Load() > 0 {
		t.Fatalf("observed %d reserve deadlocks with moderate overlap", reserveDeadlock.Load())
	}
}

func TestReserveSlotFullNotDeadlock(t *testing.T) {
	if os.Getenv("ISUCON13_MODERATE_CONVERGE_TEST") != "1" {
		t.Skip("set ISUCON13_MODERATE_CONVERGE_TEST=1")
	}
	db := testDB(t)
	dbConn = db
	ctx := context.Background()

	const startAt = int64(1700992800)
	const endAt = int64(1700996400)
	_, err := db.ExecContext(ctx,
		`UPDATE reservation_slots SET slot = 0 WHERE start_at >= ? AND end_at <= ?`, startAt, endAt)
	if err != nil {
		t.Fatalf("zero slots: %v", err)
	}

	var uid int64
	if err := db.GetContext(ctx, &uid, `SELECT id FROM users LIMIT 1`); err != nil {
		t.Fatalf("user: %v", err)
	}

	err = reserveLivestreamWriteOnce(ctx, uid, &ReserveLivestreamRequest{
		Tags: []int64{1}, Title: "t", Description: "d",
		PlaylistUrl: "https://example.com/p.m3u8", ThumbnailUrl: "https://example.com/t.webp",
		StartAt: startAt, EndAt: endAt,
	})
	d, sf, o := classifyReserveWriteErr(err)
	if err == nil || !sf || d {
		t.Fatalf("expected slot full, got err=%v deadlock=%v slotFull=%v other=%v", err, d, sf, o)
	}
}
