package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

type ReserveLivestreamRequest struct {
	Tags         []int64 `json:"tags"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	PlaylistUrl  string  `json:"playlist_url"`
	ThumbnailUrl string  `json:"thumbnail_url"`
	StartAt      int64   `json:"start_at"`
	EndAt        int64   `json:"end_at"`
}

type LivestreamViewerModel struct {
	UserID       int64 `db:"user_id" json:"user_id"`
	LivestreamID int64 `db:"livestream_id" json:"livestream_id"`
	CreatedAt    int64 `db:"created_at" json:"created_at"`
}

type LivestreamModel struct {
	ID           int64  `db:"id" json:"id"`
	UserID       int64  `db:"user_id" json:"user_id"`
	Title        string `db:"title" json:"title"`
	Description  string `db:"description" json:"description"`
	PlaylistUrl  string `db:"playlist_url" json:"playlist_url"`
	ThumbnailUrl string `db:"thumbnail_url" json:"thumbnail_url"`
	StartAt      int64  `db:"start_at" json:"start_at"`
	EndAt        int64  `db:"end_at" json:"end_at"`
}

type Livestream struct {
	ID           int64  `json:"id"`
	Owner        *User  `json:"owner"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	PlaylistUrl  string `json:"playlist_url"`
	ThumbnailUrl string `json:"thumbnail_url"`
	Tags         []Tag  `json:"tags"`
	StartAt      int64  `json:"start_at"`
	EndAt        int64  `json:"end_at"`
}

type LivestreamTagModel struct {
	ID           int64 `db:"id" json:"id"`
	LivestreamID int64 `db:"livestream_id" json:"livestream_id"`
	TagID        int64 `db:"tag_id" json:"tag_id"`
}

type ReservationSlotModel struct {
	ID      int64 `db:"id" json:"id"`
	Slot    int64 `db:"slot" json:"slot"`
	StartAt int64 `db:"start_at" json:"start_at"`
	EndAt   int64 `db:"end_at" json:"end_at"`
}

func reserveLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	defer c.Request().Body.Close()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *ReserveLivestreamRequest
	if err := decodeRequestBody(c, &req); err != nil {
		return err
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// 2023/11/25 10:00からの１年間の期間内であるかチェック
	var (
		termStartAt    = time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC)
		termEndAt      = time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC)
		reserveStartAt = time.Unix(req.StartAt, 0)
		reserveEndAt   = time.Unix(req.EndAt, 0)
	)
	if (reserveStartAt.Equal(termEndAt) || reserveStartAt.After(termEndAt)) || (reserveEndAt.Equal(termStartAt) || reserveEndAt.Before(termStartAt)) {
		return echo.NewHTTPError(http.StatusBadRequest, "bad reservation time range")
	}

	// 予約枠をみて、予約が可能か調べる
	// NOTE: 並列な予約のoverbooking防止にFOR UPDATEが必要
	var slots []*ReservationSlotModel
	if err := tx.SelectContext(ctx, &slots, "SELECT * FROM reservation_slots FORCE INDEX (idx_reservation_slots_start_at_end_at) WHERE start_at >= ? AND end_at <= ? FOR UPDATE", req.StartAt, req.EndAt); err != nil {
		c.Logger().Warnf("予約枠一覧取得でエラー発生: %+v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get reservation_slots: "+err.Error())
	}
	for _, slot := range slots {
		if os.Getenv("ISUCON13_ACCESS_LOG") == "1" {
			c.Logger().Infof("%d ~ %d予約枠の残数 = %d\n", slot.StartAt, slot.EndAt, slot.Slot)
		}
		if slot.Slot < 1 {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("予約期間 %d ~ %dに対して、予約区間 %d ~ %dが予約できません", termStartAt.Unix(), termEndAt.Unix(), req.StartAt, req.EndAt))
		}
	}

	var (
		livestreamModel = &LivestreamModel{
			UserID:       int64(userID),
			Title:        req.Title,
			Description:  req.Description,
			PlaylistUrl:  req.PlaylistUrl,
			ThumbnailUrl: req.ThumbnailUrl,
			StartAt:      req.StartAt,
			EndAt:        req.EndAt,
		}
	)

	if _, err := tx.ExecContext(ctx, "UPDATE reservation_slots SET slot = slot - 1 WHERE start_at >= ? AND end_at <= ?", req.StartAt, req.EndAt); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update reservation_slot: "+err.Error())
	}

	rs, err := tx.NamedExecContext(ctx, "INSERT INTO livestreams (user_id, title, description, playlist_url, thumbnail_url, start_at, end_at) VALUES(:user_id, :title, :description, :playlist_url, :thumbnail_url, :start_at, :end_at)", livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream: "+err.Error())
	}

	livestreamID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted livestream id: "+err.Error())
	}
	livestreamModel.ID = livestreamID

	if err := insertLivestreamTags(ctx, tx, livestreamID, req.Tags); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream tag: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}
	statsRegisterLivestream(livestreamID, int64(userID))

	// Response fill is read-only; run after commit so reservation row locks are not held during owner/tag SELECTs.
	readTx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer readTx.Rollback()

	livestreams, err := fillLivestreamsResponse(ctx, readTx, []*LivestreamModel{livestreamModel})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}
	if err := readTx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, livestreams[0])
}

// insertLivestreamTags inserts all tag associations in one statement inside tx.
func insertLivestreamTags(ctx context.Context, tx *sqlx.Tx, livestreamID int64, tagIDs []int64) error {
	if len(tagIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(tagIDs))
	args := make([]interface{}, 0, len(tagIDs)*2)
	for i, tagID := range tagIDs {
		placeholders[i] = "(?, ?)"
		args = append(args, livestreamID, tagID)
	}
	query := "INSERT INTO livestream_tags (livestream_id, tag_id) VALUES " + strings.Join(placeholders, ",")
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func searchLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	keyTagName := c.QueryParam("tag")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var livestreamModels []*LivestreamModel
	if c.QueryParam("tag") != "" {
		// タグによる取得
		tagIDList, err := lookupTagIDsByName(ctx, keyTagName)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get tags: "+err.Error())
		}
		if len(tagIDList) == 0 {
			if err := tx.Commit(); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
			}
			return c.JSON(http.StatusOK, []Livestream{})
		}

		query, params, err := sqlx.In("SELECT * FROM livestream_tags WHERE tag_id IN (?) ORDER BY livestream_id DESC", tagIDList)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to construct IN query: "+err.Error())
		}
		keyTaggedLivestreams := make([]*LivestreamTagModel, 0, len(tagIDList))
		if err := tx.SelectContext(ctx, &keyTaggedLivestreams, query, params...); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get keyTaggedLivestreams: "+err.Error())
		}

		if len(keyTaggedLivestreams) > 0 {
			livestreamIDs := make([]int64, 0, len(keyTaggedLivestreams))
			for _, keyTaggedLivestream := range keyTaggedLivestreams {
				livestreamIDs = append(livestreamIDs, keyTaggedLivestream.LivestreamID)
			}
			query, params, err := sqlx.In("SELECT * FROM livestreams WHERE id IN (?) ORDER BY id DESC", livestreamIDs)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to construct IN query: "+err.Error())
			}
			if c.QueryParam("limit") != "" {
				limit, err := strconv.Atoi(c.QueryParam("limit"))
				if err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
				}
				query += fmt.Sprintf(" LIMIT %d", limit)
			}
			query = tx.Rebind(query)
			if err := tx.SelectContext(ctx, &livestreamModels, query, params...); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
			}
		}
	} else {
		// 検索条件なし
		query := `SELECT * FROM livestreams ORDER BY id DESC`
		if c.QueryParam("limit") != "" {
			limit, err := strconv.Atoi(c.QueryParam("limit"))
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
			}
			query += fmt.Sprintf(" LIMIT %d", limit)
		}

		if err := tx.SelectContext(ctx, &livestreamModels, query); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
		}
	}

	livestreams, err := fillLivestreamsResponse(ctx, tx, livestreamModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestreams)
}

func getMyLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	if err := verifyUserSession(c); err != nil {
		return err
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var livestreamModels []*LivestreamModel
	if err := tx.SelectContext(ctx, &livestreamModels, "SELECT * FROM livestreams WHERE user_id = ?", userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}
	livestreams, err := fillLivestreamsResponse(ctx, tx, livestreamModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestreams)
}

func getUserLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	if err := verifyUserSession(c); err != nil {
		return err
	}

	username := c.Param("username")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var user UserModel
	if err := tx.GetContext(ctx, &user, "SELECT * FROM users WHERE name = ?", username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "user not found")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
		}
	}

	var livestreamModels []*LivestreamModel
	if err := tx.SelectContext(ctx, &livestreamModels, "SELECT * FROM livestreams WHERE user_id = ?", user.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}
	livestreams, err := fillLivestreamsResponse(ctx, tx, livestreamModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestreams)
}

// viewerテーブルの廃止
func enterLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id must be integer")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	viewer := LivestreamViewerModel{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		CreatedAt:    time.Now().Unix(),
	}

	if _, err := tx.NamedExecContext(ctx, "INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES(:user_id, :livestream_id, :created_at)", viewer); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream_view_history: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}
	statsOnEnter(int64(livestreamID))

	return c.NoContent(http.StatusOK)
}

func exitLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	err = withMySQLDeadlockRetry(func() error {
		return exitLivestream(ctx, userID, livestreamID)
	})
	if err != nil {
		if isMySQLDeadlock(err) {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete livestream_view_history: "+err.Error())
		}
		if strings.Contains(err.Error(), "begin transaction") {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete livestream_view_history: "+err.Error())
	}

	return c.NoContent(http.StatusOK)
}

func exitLivestream(ctx context.Context, userID int64, livestreamID int) error {
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?", userID, livestreamID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}
	statsOnExit(int64(livestreamID))
	return nil
}

func getLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	livestreamModel, err := getLivestreamModelByID(ctx, tx, int64(livestreamID))
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found livestream that has the given id")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	livestreams, err := fillLivestreamsResponse(ctx, tx, []*LivestreamModel{&livestreamModel})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}
	livestream := livestreams[0]

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestream)
}

func getLivecommentReportsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// error already check
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already check
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamModel, err := getLivestreamModelByID(ctx, tx, int64(livestreamID))
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found livestream that has the given id")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	if livestreamModel.UserID != userID {
		return echo.NewHTTPError(http.StatusForbidden, "can't get other streamer's livecomment reports")
	}

	var reportModels []*LivecommentReportModel
	if err := tx.SelectContext(ctx, &reportModels, "SELECT * FROM livecomment_reports WHERE livestream_id = ?", livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomment reports: "+err.Error())
	}

	reports, err := fillLivecommentReportsResponse(ctx, tx, reportModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment report: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, reports)
}

// fillLivestreamsResponse は、削除した fillLivestreamResponse の一覧版。
// 旧関数は配信1件ごとに users、theme、icon、livestream_tags、tags を SELECT してから Livestream を組み立てていた。
// この関数は先に owner とタグを IN でまとめて取り、構造体の組み立てだけをループで行う。1件のときもこの関数にスライスで渡す。
//
// @params
//   - ctx: リクエストの context。キャンセルされると SELECT を中断する
//   - tx: この取得に使うトランザクション
//   - livestreamModels: 埋める配信。1件取得も長さ1のスライスで渡す。0件なら SQL を出さない
//
// @return
//   - []Livestream: 引数と同じ順の API 用配信。失敗時は nil
//   - error: owner またはタグの取得に失敗したとき
func fillLivestreamsResponse(ctx context.Context, tx *sqlx.Tx, livestreamModels []*LivestreamModel) ([]Livestream, error) {
	// 戻り値の並びは引数と同じ。0件のときはこの後の IN を出さない。
	livestreams := make([]Livestream, len(livestreamModels))
	if len(livestreamModels) == 0 {
		return livestreams, nil
	}

	// SQL は出さない。配信から owner の user ID と配信 ID を集めるだけ。
	userIDs := make([]int64, 0, len(livestreamModels))
	livestreamIDs := make([]int64, 0, len(livestreamModels))
	for _, livestreamModel := range livestreamModels {
		userIDs = append(userIDs, livestreamModel.UserID)
		livestreamIDs = append(livestreamIDs, livestreamModel.ID)
	}
	// 同じ owner の配信が複数あっても、users / themes / icons は1人につき1回にする。
	userIDs = uniqueInt64(userIDs)

	// 旧関数が配信ごとに出していた users、theme、icon の SELECT をまとめて取る。
	owners, err := fillUsersByIDs(ctx, tx, userIDs)
	if err != nil {
		return nil, err
	}
	return fillLivestreamsResponseWithOwners(ctx, tx, livestreamModels, livestreamIDs, owners)
}

// fillLivestreamsResponseWithOwners は owner 用 User を既に取っているときの fillLivestreamsResponse 本体。
// owners は配信 owner の user ID をキーに含むこと。fillUsersByIDs は呼ばない。
func fillLivestreamsResponseWithOwners(ctx context.Context, tx *sqlx.Tx, livestreamModels []*LivestreamModel, livestreamIDs []int64, owners map[int64]*User) ([]Livestream, error) {
	livestreams := make([]Livestream, len(livestreamModels))
	if len(livestreamModels) == 0 {
		return livestreams, nil
	}

	uncachedModels := make([]*LivestreamModel, 0, len(livestreamModels))
	uncachedIDs := make([]int64, 0, len(livestreamModels))
	uncachedIdx := make([]int, 0, len(livestreamModels))
	for i, livestreamModel := range livestreamModels {
		if cached, ok := getLivestreamFromCache(ctx, livestreamModel.ID); ok {
			livestreams[i] = *cached
			continue
		}
		if assembled, ok := assembleLivestreamFromProcessCache(ctx, livestreamModel.ID, owners); ok {
			livestreams[i] = *assembled
			cacheLivestream(ctx, livestreamModel.ID, &livestreams[i])
			continue
		}
		uncachedModels = append(uncachedModels, livestreamModel)
		uncachedIDs = append(uncachedIDs, livestreamModel.ID)
		uncachedIdx = append(uncachedIdx, i)
	}
	if len(uncachedModels) == 0 {
		return livestreams, nil
	}

	// 旧関数が配信ごとに出していた livestream_tags と tags の SELECT をまとめて取る。
	tagsByLivestream, err := tagsByLivestreamID(ctx, tx, uncachedIDs)
	if err != nil {
		return nil, err
	}

	// 旧関数の最後と同じ構造体を、引数の順に作る。このループでは SQL を出さない。
	for j, livestreamModel := range uncachedModels {
		i := uncachedIdx[j]
		owner, ok := owners[livestreamModel.UserID]
		if !ok {
			return nil, fmt.Errorf("user not found: %d", livestreamModel.UserID)
		}
		tags := tagsByLivestream[livestreamModel.ID]
		// tagsByLivestreamID は空スライスを入れる。取りこぼしたときだけ JSON が null にならないよう [] にする。
		if tags == nil {
			tags = []Tag{}
		}
		livestreams[i] = Livestream{
			ID:           livestreamModel.ID,
			Owner:        owner,
			Title:        livestreamModel.Title,
			Tags:         tags,
			Description:  livestreamModel.Description,
			PlaylistUrl:  livestreamModel.PlaylistUrl,
			ThumbnailUrl: livestreamModel.ThumbnailUrl,
			StartAt:      livestreamModel.StartAt,
			EndAt:        livestreamModel.EndAt,
		}
		putLivestreamShellCache(livestreamShellFromModel(livestreamModel, tags, true))
		putUserProfileCache(owner, snapshotUserProfileCacheEpoch(owner.ID))
		cacheLivestream(ctx, livestreamModel.ID, &livestreams[i])
	}
	return livestreams, nil
}

// livestreamsByIDs は livestream ID の IN 取得と fillLivestreamsResponse を1回にまとめ、ID から引ける map を返す。
// livestreamIDs に重複があっても uniqueInt64 する。
func livestreamsByIDs(ctx context.Context, tx *sqlx.Tx, livestreamIDs []int64) (map[int64]Livestream, error) {
	livestreamByID := make(map[int64]Livestream, len(livestreamIDs))
	if len(livestreamIDs) == 0 {
		return livestreamByID, nil
	}
	livestreamIDs = uniqueInt64(livestreamIDs)

	missIDs := make([]int64, 0, len(livestreamIDs))
	livestreamRowModels := make([]LivestreamModel, 0, len(livestreamIDs))
	for _, id := range livestreamIDs {
		if cached, ok := getLivestreamFromCache(ctx, id); ok {
			livestreamByID[id] = *cached
			continue
		}
		if assembled, ok := assembleLivestreamFromProcessCache(ctx, id, nil); ok {
			livestreamByID[id] = *assembled
			cacheLivestream(ctx, id, assembled)
			continue
		}
		if shell, ok := getLivestreamShellFromProcessCache(id); ok {
			livestreamRowModels = append(livestreamRowModels, livestreamModelFromShell(shell))
			continue
		}
		missIDs = append(missIDs, id)
	}
	for _, id := range missIDs {
		model, err := coalescedLoadLivestreamRow(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		livestreamRowModels = append(livestreamRowModels, model)
	}
	if len(livestreamRowModels) == 0 {
		return livestreamByID, nil
	}
	livestreamPtrs := make([]*LivestreamModel, len(livestreamRowModels))
	for i := range livestreamRowModels {
		livestreamPtrs[i] = &livestreamRowModels[i]
	}
	livestreams, err := fillLivestreamsResponse(ctx, tx, livestreamPtrs)
	if err != nil {
		return nil, err
	}
	for _, ls := range livestreams {
		livestreamByID[ls.ID] = ls
	}
	return livestreamByID, nil
}

// fillUsersByIDs は、旧 fillLivestreamResponse 内の owner 取得と fillUserResponse（theme と icon）を user ID の IN にまとめる。
// icons に行が無いユーザーは NoImage.jpg のハッシュを使う。
//
// @params
//   - ctx: リクエストの context。キャンセルされると SELECT を中断する
//   - tx: この取得に使うトランザクション
//   - userIDs: owner の user ID。重複は呼び出し側で除いてある。0件なら SQL を出さない
//
// @return
//   - map[int64]*User: user ID から API 用 User。失敗時は nil
//   - error: user または theme が欠けているとき、または SELECT に失敗したとき
func fillUsersByIDs(ctx context.Context, tx *sqlx.Tx, userIDs []int64) (map[int64]*User, error) {
	users := make(map[int64]*User, len(userIDs))
	if len(userIDs) == 0 {
		return users, nil
	}

	missIDs := make([]int64, 0, len(userIDs))
	for _, id := range userIDs {
		if cached, ok := getUserFromCache(ctx, id); ok {
			users[id] = cached
			continue
		}
		if profile, ok := getUserProfileFromProcessCache(id); ok {
			users[id] = profile
			cacheUser(ctx, id, profile)
			continue
		}
		missIDs = append(missIDs, id)
	}
	if len(missIDs) == 0 {
		return users, nil
	}

	for _, id := range missIDs {
		user, err := coalescedLoadUserProfile(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		u := cloneUser(&user)
		cacheUser(ctx, id, u)
		users[id] = u
	}
	return users, nil
}

// tagsByLivestreamID は、旧 fillLivestreamResponse 内のタグ取得を配信 ID の IN にまとめる。
// 旧関数は配信ごとに livestream_tags を取り、その行ごとに tags を SELECT していた。
//
// @params
//   - ctx: リクエストの context。キャンセルされると SELECT を中断する
//   - tx: この取得に使うトランザクション
//   - livestreamIDs: タグを埋める配信 ID。0件なら SQL を出さない
//
// @return
//   - map[int64][]Tag: 配信 ID からタグ配列。タグが無い配信は空スライス。失敗時は nil
//   - error: タグ ID に対応する tags 行が無いとき、または SELECT に失敗したとき
func tagsByLivestreamID(ctx context.Context, tx *sqlx.Tx, livestreamIDs []int64) (map[int64][]Tag, error) {
	// タグが0件の配信も JSON が [] になるよう、先に空スライスを入れる。nil だと null になる。
	tags := make(map[int64][]Tag, len(livestreamIDs))
	for _, livestreamID := range livestreamIDs {
		tags[livestreamID] = []Tag{}
	}
	if len(livestreamIDs) == 0 {
		return tags, nil
	}

	// 旧: 配信ごとに SELECT * FROM livestream_tags WHERE livestream_id = ?
	// 今: 配信 ID を IN に渡し、紐づきを1回で取る。ORDER BY id は旧 SELECT * の主キー順に合わせる。
	livestreamTagModels := make([]*LivestreamTagModel, 0, len(livestreamIDs))
	if err := selectIn(ctx, tx, &livestreamTagModels, "SELECT * FROM livestream_tags WHERE livestream_id IN (?) ORDER BY id", livestreamIDs); err != nil {
		return nil, err
	}
	if len(livestreamTagModels) == 0 {
		return tags, nil
	}

	// 紐づきから tag ID を集める。同じタグが複数配信についていても tags は1回で取る。
	tagIDs := make([]int64, 0, len(livestreamTagModels))
	for _, livestreamTagModel := range livestreamTagModels {
		tagIDs = append(tagIDs, livestreamTagModel.TagID)
	}
	tagIDs = uniqueInt64(tagIDs)

	// 旧: タグ1件ごとに SELECT * FROM tags WHERE id = ?
	tagModels := make([]TagModel, 0, len(tagIDs))
	if err := selectIn(ctx, tx, &tagModels, "SELECT * FROM tags WHERE id IN (?)", tagIDs); err != nil {
		return nil, err
	}
	tagByID := make(map[int64]TagModel, len(tagModels))
	for _, tagModel := range tagModels {
		tagByID[tagModel.ID] = tagModel
	}

	tagCountByLivestream := make(map[int64]int, len(livestreamIDs))
	for _, livestreamTagModel := range livestreamTagModels {
		tagCountByLivestream[livestreamTagModel.LivestreamID]++
	}
	for livestreamID, n := range tagCountByLivestream {
		if n > 0 {
			tags[livestreamID] = make([]Tag, 0, n)
		}
	}

	// livestream_tags の id 順のまま、配信 ID ごとの配列へ足す。ここでは SQL を出さない。
	for _, livestreamTagModel := range livestreamTagModels {
		tagModel, ok := tagByID[livestreamTagModel.TagID]
		if !ok {
			// 旧 GetContext が sql.ErrNoRows になる欠落と同じ失敗にする。
			return nil, sql.ErrNoRows
		}
		tags[livestreamTagModel.LivestreamID] = append(tags[livestreamTagModel.LivestreamID], Tag{
			ID:   tagModel.ID,
			Name: tagModel.Name,
		})
	}
	return tags, nil
}

// userFillModel は fillUsersByIDs 用の users 行（password 列を読まない）。
type userFillModel struct {
	ID          int64  `db:"id"`
	Name        string `db:"name"`
	DisplayName string `db:"display_name"`
	Description string `db:"description"`
}

type themeFillModel struct {
	ID       int64 `db:"id"`
	UserID   int64 `db:"user_id"`
	DarkMode bool  `db:"dark_mode"`
}

// iconHashModel は icons から user_id と icon_hash だけを受ける。SELECT user_id, icon_hash のスキャン先。
//
// @params
//   - UserID: icons.user_id
//   - IconHash: icons.icon_hash。行が無いユーザーはこの構造体に入らない
type iconHashModel struct {
	UserID   int64  `db:"user_id"`
	IconHash string `db:"icon_hash"`
}

func uniqueInt64(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// selectIn は IN (?) を ID の個数だけ広げ、その SELECT を1回実行して dest にスキャンする。
// sqlx.In はまだクエリを実行しない。Rebind のあと SelectContext が実行する。
//
// @params
//   - ctx: リクエストの context。キャンセルされると SELECT を中断する
//   - tx: この取得に使うトランザクション
//   - dest: 結果のスキャン先。呼び出し側が型 T のスライスを渡す
//   - query: IN (?) を1つ含む SQL。実行前の文字列
//   - ids: IN に入れる ID。空だと sqlx.In がエラーになる
//
// @return
//   - error: SQL の組み立てか実行に失敗したとき。0件はエラーにしない
func selectIn[T any](ctx context.Context, tx *sqlx.Tx, dest *[]T, query string, ids []int64) error {
	q, args, err := sqlx.In(query, ids)
	if err != nil {
		return err
	}
	q = tx.Rebind(q)
	return tx.SelectContext(ctx, dest, q, args...)
}
