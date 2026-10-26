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
	livestreams, err := fillLivestreamsResponse(ctx, dbConn, []*LivestreamModel{livestreamModel})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
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

	// Read-only path: no transaction (same as getNgwords / payment GET). Fill uses autocommit
	// SELECTs; no cross-statement consistency requirement beyond normal bench tolerance.
	var livestreamModels []*LivestreamModel
	if c.QueryParam("tag") != "" {
		// タグによる取得
		tagIDList, err := lookupTagIDsByName(ctx, keyTagName)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get tags: "+err.Error())
		}
		if len(tagIDList) == 0 {
			return c.JSON(http.StatusOK, []Livestream{})
		}

		limitParam := c.QueryParam("limit")
		if limitParam != "" {
			limit, err := strconv.Atoi(limitParam)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
			}
			// LIMIT in SQL: avoid scanning all tag associations when only top-N livestreams are needed.
			query, params, err := sqlx.In(`
				SELECT * FROM livestreams
				WHERE id IN (
					SELECT DISTINCT livestream_id FROM livestream_tags WHERE tag_id IN (?)
				)
				ORDER BY id DESC`, tagIDList)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to construct IN query: "+err.Error())
			}
			query += fmt.Sprintf(" LIMIT %d", limit)
			query = dbConn.Rebind(query)
			if err := dbConn.SelectContext(ctx, &livestreamModels, query, params...); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
			}
		} else {
			// VisitTop tag search (no limit): two queries + literal IN — better MySQL plan than materialized subquery.
			query, params, err := sqlx.In("SELECT * FROM livestream_tags WHERE tag_id IN (?) ORDER BY livestream_id DESC", tagIDList)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to construct IN query: "+err.Error())
			}
			keyTaggedLivestreams := make([]*LivestreamTagModel, 0, len(tagIDList))
			if err := dbConn.SelectContext(ctx, &keyTaggedLivestreams, dbConn.Rebind(query), params...); err != nil {
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
				query = dbConn.Rebind(query)
				if err := dbConn.SelectContext(ctx, &livestreamModels, query, params...); err != nil {
					return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
				}
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

		if err := dbConn.SelectContext(ctx, &livestreamModels, query); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
		}
	}

	livestreams, err := fillLivestreamsResponse(ctx, dbConn, livestreamModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestreams)
}

func getMyLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	if err := verifyUserSession(c); err != nil {
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var livestreamModels []*LivestreamModel
	if err := dbConn.SelectContext(ctx, &livestreamModels, "SELECT * FROM livestreams WHERE user_id = ?", userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}
	livestreams, err := fillLivestreamsResponse(ctx, dbConn, livestreamModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
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

	var user UserModel
	if err := dbConn.GetContext(ctx, &user, "SELECT * FROM users WHERE name = ?", username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "user not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	var livestreamModels []*LivestreamModel
	if err := dbConn.SelectContext(ctx, &livestreamModels, "SELECT * FROM livestreams WHERE user_id = ?", user.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}
	livestreams, err := fillLivestreamsResponse(ctx, dbConn, livestreamModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
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

	viewer := LivestreamViewerModel{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		CreatedAt:    time.Now().Unix(),
	}

	// Single-row INSERT needs no explicit transaction (same atomicity as autocommit).
	if _, err := dbConn.NamedExecContext(ctx, "INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES(:user_id, :livestream_id, :created_at)", viewer); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream_view_history: "+err.Error())
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
	// Single-row DELETE needs no explicit transaction (same as enter autocommit INSERT).
	if _, err := dbConn.ExecContext(ctx, "DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?", userID, livestreamID); err != nil {
		return err
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
	lsID := int64(livestreamID)

	if cached, ok := getLivestreamFromCache(ctx, lsID); ok {
		return c.JSON(http.StatusOK, cached)
	}
	if assembled, ok := assembleLivestreamFromProcessCache(ctx, lsID, nil); ok {
		cacheLivestream(ctx, lsID, assembled)
		return c.JSON(http.StatusOK, assembled)
	}

	livestreamModel, err := getLivestreamModelByID(ctx, dbConn, lsID)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found livestream that has the given id")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	livestreams, err := fillLivestreamsResponse(ctx, dbConn, []*LivestreamModel{&livestreamModel})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}
	livestream := livestreams[0]

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

	// error already check
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already check
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamModel, err := getLivestreamModelByID(ctx, dbConn, int64(livestreamID))
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
	if err := dbConn.SelectContext(ctx, &reportModels, "SELECT * FROM livecomment_reports WHERE livestream_id = ?", livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomment reports: "+err.Error())
	}

	reports, err := fillLivecommentReportsResponse(ctx, dbConn, reportModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment report: "+err.Error())
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
func fillLivestreamsResponse(ctx context.Context, q readQueryer, livestreamModels []*LivestreamModel) ([]Livestream, error) {
	// 戻り値の並びは引数と同じ。0件のときはこの後の IN を出さない。
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
		if assembled, ok := assembleLivestreamFromProcessCache(ctx, livestreamModel.ID, nil); ok {
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

	userIDs := make([]int64, 0, len(uncachedModels))
	for _, livestreamModel := range uncachedModels {
		userIDs = append(userIDs, livestreamModel.UserID)
	}
	userIDs = uniqueInt64(userIDs)

	owners, err := fillUsersByIDs(ctx, q, userIDs)
	if err != nil {
		return nil, err
	}
	return fillLivestreamsResponseUncached(ctx, q, livestreams, uncachedModels, uncachedIDs, uncachedIdx, owners)
}

// fillLivestreamsResponseUncached completes rows that missed request/process cache (tags SQL + assembly).
func fillLivestreamsResponseUncached(ctx context.Context, q readQueryer, livestreams []Livestream, uncachedModels []*LivestreamModel, uncachedIDs []int64, uncachedIdx []int, owners map[int64]*User) ([]Livestream, error) {
	tagsByLivestream, err := tagsByLivestreamID(ctx, q, uncachedIDs)
	if err != nil {
		return nil, err
	}
	for j, livestreamModel := range uncachedModels {
		i := uncachedIdx[j]
		owner, ok := owners[livestreamModel.UserID]
		if !ok {
			return nil, fmt.Errorf("user not found: %d", livestreamModel.UserID)
		}
		tags := tagsByLivestream[livestreamModel.ID]
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

// fillLivestreamsResponseWithOwners は owner 用 User を既に取っているときの fillLivestreamsResponse 本体。
// owners は配信 owner の user ID をキーに含むこと。fillUsersByIDs は呼ばない。
func fillLivestreamsResponseWithOwners(ctx context.Context, q readQueryer, livestreamModels []*LivestreamModel, livestreamIDs []int64, owners map[int64]*User) ([]Livestream, error) {
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

	return fillLivestreamsResponseUncached(ctx, q, livestreams, uncachedModels, uncachedIDs, uncachedIdx, owners)
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
func fillUsersByIDs(ctx context.Context, q readQueryer, userIDs []int64) (map[int64]*User, error) {
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

	missIDs = uniqueInt64(missIDs)
	if len(missIDs) == 1 {
		user, err := coalescedLoadUserProfile(ctx, q, missIDs[0])
		if err != nil {
			return nil, err
		}
		u := cloneUser(&user)
		cacheUser(ctx, missIDs[0], u)
		users[missIDs[0]] = u
		return users, nil
	}

	loaded, err := loadUserProfilesFromDBByIDs(ctx, q, missIDs)
	if err != nil {
		return nil, err
	}
	for id, user := range loaded {
		fillEpoch := snapshotUserProfileCacheEpoch(id)
		putUserProfileCache(cloneUser(&user), fillEpoch)
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
func tagsByLivestreamID(ctx context.Context, q readQueryer, livestreamIDs []int64) (map[int64][]Tag, error) {
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
	if err := selectIn(ctx, q, &livestreamTagModels, "SELECT * FROM livestream_tags WHERE livestream_id IN (?) ORDER BY id", livestreamIDs); err != nil {
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

	// tags 行は initialize まで変わらない。GET /api/tag と同じ process キャッシュを使い IN SELECT を省く。
	tagByID, err := tagByIDMapFromCache(ctx)
	if err != nil {
		return nil, err
	}
	for _, tagID := range tagIDs {
		if _, ok := tagByID[tagID]; !ok {
			return nil, sql.ErrNoRows
		}
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
		tag := tagByID[livestreamTagModel.TagID]
		tags[livestreamTagModel.LivestreamID] = append(tags[livestreamTagModel.LivestreamID], tag)
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

// loadLivestreamModelsByIDs loads livestreams rows for process-cache misses in one IN query.
func loadLivestreamModelsByIDs(ctx context.Context, q readQueryer, livestreamIDs []int64) ([]LivestreamModel, error) {
	livestreamIDs = uniqueInt64(livestreamIDs)
	if len(livestreamIDs) == 0 {
		return []LivestreamModel{}, nil
	}
	var fetched []LivestreamModel
	if err := selectIn(ctx, q, &fetched, "SELECT * FROM livestreams WHERE id IN (?)", livestreamIDs); err != nil {
		return nil, err
	}
	if len(fetched) != len(livestreamIDs) {
		return nil, sql.ErrNoRows
	}
	for i := range fetched {
		putLivestreamRowCache(&fetched[i])
	}
	return fetched, nil
}

// appendLivestreamRowsOnCacheMiss extends cached rows with DB loads for shell-cache misses.
func appendLivestreamRowsOnCacheMiss(ctx context.Context, q readQueryer, cached []LivestreamModel, missIDs []int64) ([]LivestreamModel, error) {
	missIDs = uniqueInt64(missIDs)
	if len(missIDs) == 0 {
		return cached, nil
	}
	if len(missIDs) == 1 {
		model, err := coalescedLoadLivestreamRow(ctx, q, missIDs[0])
		if err != nil {
			return nil, err
		}
		return append(cached, model), nil
	}
	fetched, err := loadLivestreamModelsByIDs(ctx, q, missIDs)
	if err != nil {
		return nil, err
	}
	return append(cached, fetched...), nil
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
func selectIn[T any](ctx context.Context, q readQueryer, dest *[]T, query string, ids []int64) error {
	qi, args, err := sqlx.In(query, ids)
	if err != nil {
		return err
	}
	qi = q.Rebind(qi)
	return q.SelectContext(ctx, dest, qi, args...)
}
