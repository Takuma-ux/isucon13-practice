package main

import (
	"context"
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
)

// ISUCON13_FILL_REPORTS_TEST=1 で DB に接続し、moderate 後に残る orphan report 行を一覧がスキップすることを確認する。
func TestFillLivecommentReportsSkipsDeletedLivecomment(t *testing.T) {
	if os.Getenv("ISUCON13_FILL_REPORTS_TEST") != "1" {
		t.Skip("set ISUCON13_FILL_REPORTS_TEST=1 to run")
	}

	db, err := connectDB(nil)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	var reportID int64
	if err := tx.GetContext(ctx, &reportID, `
		SELECT r.id FROM livecomment_reports r
		LEFT JOIN livecomments lc ON lc.id = r.livecomment_id
		WHERE lc.id IS NULL
		LIMIT 1`); err != nil {
		t.Skip("no orphan livecomment_report row in DB; create one via moderate then re-run")
	}

	var rm LivecommentReportModel
	if err := tx.GetContext(ctx, &rm, "SELECT * FROM livecomment_reports WHERE id = ?", reportID); err != nil {
		t.Fatalf("get report: %v", err)
	}

	reports, err := fillLivecommentReportsResponse(ctx, tx, []*LivecommentReportModel{&rm})
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("expected empty slice for orphan report, got %d", len(reports))
	}
}

func TestFillLivecommentReportsEmptyInput(t *testing.T) {
	ctx := context.Background()
	var tx *sqlx.Tx
	reports, err := fillLivecommentReportsResponse(ctx, tx, nil)
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if reports == nil || len(reports) != 0 {
		t.Fatalf("expected empty non-nil slice, got %#v", reports)
	}
}
