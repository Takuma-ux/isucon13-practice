package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

type PostLivecommentRequest struct {
	Comment string `json:"comment"`
	Tip     int64  `json:"tip"`
}

type LivecommentModel struct {
	ID           int64  `db:"id"`
	UserID       int64  `db:"user_id"`
	LivestreamID int64  `db:"livestream_id"`
	Comment      string `db:"comment"`
	Tip          int64  `db:"tip"`
	CreatedAt    int64  `db:"created_at"`
}

type Livecomment struct {
	ID         int64      `json:"id"`
	User       User       `json:"user"`
	Livestream Livestream `json:"livestream"`
	Comment    string     `json:"comment"`
	Tip        int64      `json:"tip"`
	CreatedAt  int64      `json:"created_at"`
}

type LivecommentReport struct {
	ID          int64       `json:"id"`
	Reporter    User        `json:"reporter"`
	Livecomment Livecomment `json:"livecomment"`
	CreatedAt   int64       `json:"created_at"`
}

type LivecommentReportModel struct {
	ID            int64 `db:"id"`
	UserID        int64 `db:"user_id"`
	LivestreamID  int64 `db:"livestream_id"`
	LivecommentID int64 `db:"livecomment_id"`
	CreatedAt     int64 `db:"created_at"`
}

type ModerateRequest struct {
	NGWord string `json:"ng_word"`
}

type NGWord struct {
	ID           int64  `json:"id" db:"id"`
	UserID       int64  `json:"user_id" db:"user_id"`
	LivestreamID int64  `json:"livestream_id" db:"livestream_id"`
	Word         string `json:"word" db:"word"`
	CreatedAt    int64  `json:"created_at" db:"created_at"`
}

func getLivecommentsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
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

	query := "SELECT * FROM livecomments WHERE livestream_id = ? ORDER BY created_at DESC"
	if c.QueryParam("limit") != "" {
		limit, err := strconv.Atoi(c.QueryParam("limit"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
		}
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	livecommentModels := []LivecommentModel{}
	err = tx.SelectContext(ctx, &livecommentModels, query, livestreamID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusOK, []*Livecomment{})
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomments: "+err.Error())
	}

	livecomments, err := fillLivecommentsForLivestream(ctx, tx, livecommentModels, int64(livestreamID))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fil livecomments: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livecomments)
}

func getNgwords(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	if err := verifyUserSession(c); err != nil {
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

	var ngWords []*NGWord
	if err := dbConn.SelectContext(ctx, &ngWords, "SELECT * FROM ng_words WHERE user_id = ? AND livestream_id = ? ORDER BY created_at DESC", userID, livestreamID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusOK, []*NGWord{})
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get NG words: "+err.Error())
		}
	}

	return c.JSON(http.StatusOK, ngWords)
}

func postLivecommentHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	defer c.Request().Body.Close()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *PostLivecommentRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var livestreamModel LivestreamModel
	if err := tx.GetContext(ctx, &livestreamModel, "SELECT * FROM livestreams WHERE id = ?", livestreamID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
		}
	}

	// スパム判定 (Go側でstrings.Containsを使い、DBクエリを削減)
	var ngwords []*NGWord
	if err := tx.SelectContext(ctx, &ngwords, "SELECT id, user_id, livestream_id, word FROM ng_words WHERE user_id = ? AND livestream_id = ?", livestreamModel.UserID, livestreamModel.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get NG words: "+err.Error())
	}

	for _, ngword := range ngwords {
		if strings.Contains(req.Comment, ngword.Word) {
			return echo.NewHTTPError(http.StatusBadRequest, "このコメントがスパム判定されました")
		}
	}

	now := time.Now().Unix()
	livecommentModel := LivecommentModel{
		UserID:       userID,
		LivestreamID: int64(livestreamID),
		Comment:      req.Comment,
		Tip:          req.Tip,
		CreatedAt:    now,
	}

	rs, err := tx.NamedExecContext(ctx, "INSERT INTO livecomments (user_id, livestream_id, comment, tip, created_at) VALUES (:user_id, :livestream_id, :comment, :tip, :created_at)", livecommentModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livecomment: "+err.Error())
	}

	livecommentID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted livecomment id: "+err.Error())
	}
	livecommentModel.ID = livecommentID

	livecomment, err := fillLivecommentResponseWithLivestream(ctx, tx, livecommentModel, livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, livecomment)
}

func reportLivecommentHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	livecommentID, err := strconv.Atoi(c.Param("livecomment_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livecomment_id in path must be integer")
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var livestreamModel LivestreamModel
	if err := tx.GetContext(ctx, &livestreamModel, "SELECT * FROM livestreams WHERE id = ?", livestreamID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
		}
	}

	var livecommentModel LivecommentModel
	if err := tx.GetContext(ctx, &livecommentModel, "SELECT * FROM livecomments WHERE id = ?", livecommentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "livecomment not found")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomment: "+err.Error())
		}
	}

	now := time.Now().Unix()
	reportModel := LivecommentReportModel{
		UserID:        int64(userID),
		LivestreamID:  int64(livestreamID),
		LivecommentID: int64(livecommentID),
		CreatedAt:     now,
	}
	rs, err := tx.NamedExecContext(ctx, "INSERT INTO livecomment_reports(user_id, livestream_id, livecomment_id, created_at) VALUES (:user_id, :livestream_id, :livecomment_id, :created_at)", &reportModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livecomment report: "+err.Error())
	}
	reportID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted livecomment report id: "+err.Error())
	}
	reportModel.ID = reportID

	report, err := fillLivecommentReportResponseWithLivestream(ctx, tx, reportModel, livecommentModel, livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment report: "+err.Error())
	}
	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, report)
}

const spamLivecommentDeleteBatchSize = 500

// deleteLivecommentsHittingNgWords は moderate 用。当該配信の全 NG ワードにマッチするコメントを削除する。
// 単一の広い DELETE より、id 順にバッチで FOR UPDATE → PK 削除し、ロック順序と保持時間を抑える。
func deleteLivecommentsHittingNgWords(ctx context.Context, tx *sqlx.Tx, livestreamID int) error {
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

	for {
		var ids []int64
		if err := tx.SelectContext(ctx, &ids, selectIDs, livestreamID, livestreamID, spamLivecommentDeleteBatchSize); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}

		query, args, err := sqlx.In("DELETE FROM livecomments WHERE id IN (?)", ids)
		if err != nil {
			return err
		}
		query = tx.Rebind(query)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
}

func moderateLivestream(ctx context.Context, userID int64, livestreamID int, ngWord string) (wordID int64, err error) {
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errModerateBegin, err)
	}
	defer tx.Rollback()

	var ownedLivestreams []LivestreamModel
	if err := tx.SelectContext(ctx, &ownedLivestreams, "SELECT * FROM livestreams WHERE id = ? AND user_id = ?", livestreamID, userID); err != nil {
		return 0, fmt.Errorf("%w: %w", errModerateGetLivestreams, err)
	}
	if len(ownedLivestreams) == 0 {
		return 0, errModerateNotOwner
	}

	rs, err := tx.NamedExecContext(ctx, "INSERT INTO ng_words(user_id, livestream_id, word, created_at) VALUES (:user_id, :livestream_id, :word, :created_at)", &NGWord{
		UserID:       userID,
		LivestreamID: int64(livestreamID),
		Word:         ngWord,
		CreatedAt:    time.Now().Unix(),
	})
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errModerateInsertNGWord, err)
	}

	wordID, err = rs.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errModerateLastInsertID, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("%w: %w", errModerateCommit, err)
	}

	// NG ワード登録だけ先に見えるようにし、スパム削除は別トランザクションで行う。
	// viewer-report が moderate による DELETE と競合して 404 になる窓を狭める。
	scheduleDeleteLivecommentsHittingNgWords(livestreamID)
	return wordID, nil
}

func scheduleDeleteLivecommentsHittingNgWords(livestreamID int) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for attempt := 0; attempt < maxMySQLDeadlockRetries; attempt++ {
			tx, err := dbConn.BeginTxx(ctx, nil)
			if err != nil {
				return
			}
			err = deleteLivecommentsHittingNgWords(ctx, tx, livestreamID)
			if err != nil {
				tx.Rollback()
				if isMySQLDeadlock(err) {
					continue
				}
				return
			}
			if err := tx.Commit(); err != nil {
				tx.Rollback()
				if isMySQLDeadlock(err) {
					continue
				}
				return
			}
			return
		}
	}()
}

var (
	errModerateNotOwner      = errors.New("moderate: not stream owner")
	errModerateBegin         = errors.New("moderate: begin")
	errModerateGetLivestreams = errors.New("moderate: get livestreams")
	errModerateInsertNGWord  = errors.New("moderate: insert ng word")
	errModerateLastInsertID  = errors.New("moderate: last insert id")
	errModerateDeleteSpam    = errors.New("moderate: delete spam")
	errModerateCommit        = errors.New("moderate: commit")
)

// NGワードを登録
func moderateHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	defer c.Request().Body.Close()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *ModerateRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	var wordID int64
	var lastErr error
	for attempt := 0; attempt < maxMySQLDeadlockRetries; attempt++ {
		wordID, lastErr = moderateLivestream(ctx, userID, livestreamID, req.NGWord)
		if lastErr == nil {
			return c.JSON(http.StatusCreated, map[string]interface{}{
				"word_id": wordID,
			})
		}
		if errors.Is(lastErr, errModerateNotOwner) {
			return echo.NewHTTPError(http.StatusBadRequest, "A streamer can't moderate livestreams that other streamers own")
		}
		if isMySQLDeadlock(lastErr) {
			continue
		}
		return moderateHandlerError(lastErr)
	}
	return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete old livecomments that hit spams: "+lastErr.Error())
}

func moderateHandlerError(err error) *echo.HTTPError {
	switch {
	case errors.Is(err, errModerateBegin):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	case errors.Is(err, errModerateGetLivestreams):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	case errors.Is(err, errModerateInsertNGWord):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert new NG word: "+err.Error())
	case errors.Is(err, errModerateLastInsertID):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted NG word id: "+err.Error())
	case errors.Is(err, errModerateDeleteSpam):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete old livecomments that hit spams: "+err.Error())
	case errors.Is(err, errModerateCommit):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
}

// fillLivecommentsForLivestream は GET /api/livestream/:livestream_id/livecomment 用。
// パスの livestream を1回取り、投稿者と配信者の user ID をまとめて fillUsersByIDs し、
// livestreamsByIDs 経由の2回目の owner 用 users/themes/icons SELECT を避ける。
func fillLivecommentsForLivestream(ctx context.Context, tx *sqlx.Tx, livecommentModels []LivecommentModel, livestreamID int64) ([]Livecomment, error) {
	if len(livecommentModels) == 0 {
		return []Livecomment{}, nil
	}

	var livestream Livestream
	if cached, ok := getLivestreamFromCache(ctx, livestreamID); ok {
		livestream = *cached
	} else {
		var livestreamModel LivestreamModel
		if err := tx.GetContext(ctx, &livestreamModel, "SELECT * FROM livestreams WHERE id = ?", livestreamID); err != nil {
			return nil, err
		}

		userIDs := make([]int64, 0, len(livecommentModels)+1)
		for _, m := range livecommentModels {
			userIDs = append(userIDs, m.UserID)
		}
		userIDs = append(userIDs, livestreamModel.UserID)
		userIDs = uniqueInt64(userIDs)

		usersByID, err := fillUsersByIDs(ctx, tx, userIDs)
		if err != nil {
			return nil, err
		}

		livestreams, err := fillLivestreamsResponseWithOwners(ctx, tx, []*LivestreamModel{&livestreamModel}, []int64{livestreamID}, usersByID)
		if err != nil {
			return nil, err
		}
		if len(livestreams) != 1 {
			return nil, sql.ErrNoRows
		}
		livestream = livestreams[0]
	}

	userIDs := make([]int64, 0, len(livecommentModels))
	for _, m := range livecommentModels {
		userIDs = append(userIDs, m.UserID)
	}
	userIDs = uniqueInt64(userIDs)

	usersByID, err := fillUsersByIDs(ctx, tx, userIDs)
	if err != nil {
		return nil, err
	}

	livecomments := make([]Livecomment, len(livecommentModels))
	for i, m := range livecommentModels {
		user, ok := usersByID[m.UserID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		livecomments[i] = Livecomment{
			ID:         m.ID,
			User:       user,
			Livestream: livestream,
			Comment:    m.Comment,
			Tip:        m.Tip,
			CreatedAt:  m.CreatedAt,
		}
	}
	return livecomments, nil
}

// fillLivecommentsResponse は、コメント一覧の投稿者 User と配信 Livestream を IN でまとめて埋める。
// 1件だけ必要なときは fillLivecommentResponse がこの関数を1件スライスで呼ぶ。
func fillLivecommentsResponse(ctx context.Context, tx *sqlx.Tx, livecommentModels []LivecommentModel) ([]Livecomment, error) {
	if len(livecommentModels) == 0 {
		return []Livecomment{}, nil
	}

	userIDs := make([]int64, 0, len(livecommentModels))
	livestreamIDs := make([]int64, 0, len(livecommentModels))
	for _, m := range livecommentModels {
		userIDs = append(userIDs, m.UserID)
		livestreamIDs = append(livestreamIDs, m.LivestreamID)
	}
	userIDs = uniqueInt64(userIDs)

	usersByID, err := fillUsersByIDs(ctx, tx, userIDs)
	if err != nil {
		return nil, err
	}

	livestreamByID, err := livestreamsByIDs(ctx, tx, livestreamIDs)
	if err != nil {
		return nil, err
	}

	livecomments := make([]Livecomment, len(livecommentModels))
	for i, m := range livecommentModels {
		user, ok := usersByID[m.UserID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		livestream, ok := livestreamByID[m.LivestreamID]
		if !ok {
			return nil, fmt.Errorf("livestream not found: %d", m.LivestreamID)
		}
		livecomments[i] = Livecomment{
			ID:         m.ID,
			User:       user,
			Livestream: livestream,
			Comment:    m.Comment,
			Tip:        m.Tip,
			CreatedAt:  m.CreatedAt,
		}
	}
	return livecomments, nil
}

func fillLivecommentResponse(ctx context.Context, tx *sqlx.Tx, livecommentModel LivecommentModel) (Livecomment, error) {
	livecomments, err := fillLivecommentsResponse(ctx, tx, []LivecommentModel{livecommentModel})
	if err != nil {
		return Livecomment{}, err
	}
	return livecomments[0], nil
}

// fillLivecommentResponseWithLivestream は POST コメント用。ハンドラが既に取得した livestream 行を
// fillLivestreamsResponseWithOwners に渡し、livestreams の再 SELECT と owner 用の2回目 fillUsersByIDs を避ける。
func fillLivecommentResponseWithLivestream(ctx context.Context, tx *sqlx.Tx, livecommentModel LivecommentModel, livestreamModel LivestreamModel) (Livecomment, error) {
	if livecommentModel.LivestreamID != livestreamModel.ID {
		return Livecomment{}, fmt.Errorf("livestream not found: %d", livecommentModel.LivestreamID)
	}

	userIDs := uniqueInt64([]int64{livecommentModel.UserID, livestreamModel.UserID})
	usersByID, err := fillUsersByIDs(ctx, tx, userIDs)
	if err != nil {
		return Livecomment{}, err
	}

	livestreams, err := fillLivestreamsResponseWithOwners(ctx, tx, []*LivestreamModel{&livestreamModel}, []int64{livestreamModel.ID}, usersByID)
	if err != nil {
		return Livecomment{}, err
	}
	if len(livestreams) != 1 {
		return Livecomment{}, sql.ErrNoRows
	}
	livestream := livestreams[0]

	user, ok := usersByID[livecommentModel.UserID]
	if !ok {
		return Livecomment{}, sql.ErrNoRows
	}

	return Livecomment{
		ID:         livecommentModel.ID,
		User:       user,
		Livestream: livestream,
		Comment:    livecommentModel.Comment,
		Tip:        livecommentModel.Tip,
		CreatedAt:  livecommentModel.CreatedAt,
	}, nil
}

// fillLivecommentReportsResponse は GET 通報一覧用。
// 通報者・コメント投稿者・配信者の user ID をまとめて fillUsersByIDs し、
// fillLivecommentsResponse 経由の2回目の users/themes/icons SELECT を避ける。
func fillLivecommentReportsResponse(ctx context.Context, tx *sqlx.Tx, reportModels []*LivecommentReportModel) ([]LivecommentReport, error) {
	if len(reportModels) == 0 {
		return []LivecommentReport{}, nil
	}

	reporterIDs := make([]int64, 0, len(reportModels))
	livecommentIDs := make([]int64, 0, len(reportModels))
	for _, rm := range reportModels {
		reporterIDs = append(reporterIDs, rm.UserID)
		livecommentIDs = append(livecommentIDs, rm.LivecommentID)
	}
	reporterIDs = uniqueInt64(reporterIDs)
	livecommentIDs = uniqueInt64(livecommentIDs)

	var livecommentRowModels []LivecommentModel
	if err := selectIn(ctx, tx, &livecommentRowModels, "SELECT * FROM livecomments WHERE id IN (?)", livecommentIDs); err != nil {
		return nil, err
	}
	// moderate でコメントだけ削除され livecomment_reports が残ることがある。一覧は存在するコメントだけ返す。
	if len(livecommentRowModels) == 0 {
		return []LivecommentReport{}, nil
	}

	livestreamIDs := make([]int64, 0, len(livecommentRowModels))
	for _, m := range livecommentRowModels {
		livestreamIDs = append(livestreamIDs, m.LivestreamID)
	}
	livestreamIDs = uniqueInt64(livestreamIDs)

	var livestreamRowModels []LivestreamModel
	if err := selectIn(ctx, tx, &livestreamRowModels, "SELECT * FROM livestreams WHERE id IN (?)", livestreamIDs); err != nil {
		return nil, err
	}
	livestreamModelByID := make(map[int64]LivestreamModel, len(livestreamRowModels))
	for _, ls := range livestreamRowModels {
		livestreamModelByID[ls.ID] = ls
	}

	filteredComments := make([]LivecommentModel, 0, len(livecommentRowModels))
	for _, m := range livecommentRowModels {
		if _, ok := livestreamModelByID[m.LivestreamID]; ok {
			filteredComments = append(filteredComments, m)
		}
	}
	if len(filteredComments) == 0 {
		return []LivecommentReport{}, nil
	}
	livecommentRowModels = filteredComments

	userIDs := make([]int64, 0, len(reporterIDs)+len(livecommentRowModels)+len(livestreamRowModels))
	userIDs = append(userIDs, reporterIDs...)
	for _, m := range livecommentRowModels {
		userIDs = append(userIDs, m.UserID)
	}
	for _, ls := range livestreamRowModels {
		userIDs = append(userIDs, ls.UserID)
	}
	userIDs = uniqueInt64(userIDs)

	usersByID, err := fillUsersByIDs(ctx, tx, userIDs)
	if err != nil {
		return nil, err
	}

	livestreamPtrs := make([]*LivestreamModel, len(livestreamRowModels))
	for i := range livestreamRowModels {
		livestreamPtrs[i] = &livestreamRowModels[i]
	}
	livestreams, err := fillLivestreamsResponseWithOwners(ctx, tx, livestreamPtrs, livestreamIDs, usersByID)
	if err != nil {
		return nil, err
	}
	livestreamByID := make(map[int64]Livestream, len(livestreams))
	for _, ls := range livestreams {
		livestreamByID[ls.ID] = ls
	}

	livecommentByID := make(map[int64]Livecomment, len(livecommentRowModels))
	for _, m := range livecommentRowModels {
		user, ok := usersByID[m.UserID]
		if !ok {
			continue
		}
		livestream, ok := livestreamByID[m.LivestreamID]
		if !ok {
			continue
		}
		livecommentByID[m.ID] = Livecomment{
			ID:         m.ID,
			User:       user,
			Livestream: livestream,
			Comment:    m.Comment,
			Tip:        m.Tip,
			CreatedAt:  m.CreatedAt,
		}
	}

	reports := make([]LivecommentReport, 0, len(reportModels))
	for _, rm := range reportModels {
		livecomment, ok := livecommentByID[rm.LivecommentID]
		if !ok {
			continue
		}
		reporter, ok := usersByID[rm.UserID]
		if !ok {
			continue
		}
		reports = append(reports, LivecommentReport{
			ID:          rm.ID,
			Reporter:    reporter,
			Livecomment: livecomment,
			CreatedAt:   rm.CreatedAt,
		})
	}
	return reports, nil
}

// fillLivecommentReportResponseWithLivestream は POST 通報用。ハンドラが既に取得した
// livestream / livecomment 行を fillLivecommentResponseWithLivestream に渡し、再 SELECT を避ける。
func fillLivecommentReportResponseWithLivestream(ctx context.Context, tx *sqlx.Tx, reportModel LivecommentReportModel, livecommentModel LivecommentModel, livestreamModel LivestreamModel) (LivecommentReport, error) {
	if livecommentModel.LivestreamID != livestreamModel.ID {
		return LivecommentReport{}, fmt.Errorf("livestream not found: %d", livecommentModel.LivestreamID)
	}

	userIDs := uniqueInt64([]int64{reportModel.UserID, livecommentModel.UserID, livestreamModel.UserID})
	usersByID, err := fillUsersByIDs(ctx, tx, userIDs)
	if err != nil {
		return LivecommentReport{}, err
	}

	livestreams, err := fillLivestreamsResponseWithOwners(ctx, tx, []*LivestreamModel{&livestreamModel}, []int64{livestreamModel.ID}, usersByID)
	if err != nil {
		return LivecommentReport{}, err
	}
	if len(livestreams) != 1 {
		return LivecommentReport{}, sql.ErrNoRows
	}

	user, ok := usersByID[livecommentModel.UserID]
	if !ok {
		return LivecommentReport{}, sql.ErrNoRows
	}
	reporter, ok := usersByID[reportModel.UserID]
	if !ok {
		return LivecommentReport{}, sql.ErrNoRows
	}

	return LivecommentReport{
		ID:          reportModel.ID,
		Reporter:    reporter,
		Livecomment: Livecomment{
			ID:         livecommentModel.ID,
			User:       user,
			Livestream: livestreams[0],
			Comment:    livecommentModel.Comment,
			Tip:        livecommentModel.Tip,
			CreatedAt:  livecommentModel.CreatedAt,
		},
		CreatedAt: reportModel.CreatedAt,
	}, nil
}
