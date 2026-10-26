package main

import (
	"context"
	"database/sql"
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
	ID         int64       `json:"id"`
	User       *User       `json:"user"`
	Livestream *Livestream `json:"livestream"`
	Comment    string     `json:"comment"`
	Tip        int64      `json:"tip"`
	CreatedAt  int64      `json:"created_at"`
}

type LivecommentReport struct {
	ID          int64       `json:"id"`
	Reporter    *User       `json:"reporter"`
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

	// Read-only path: no transaction (same tolerance as search GET). Fill uses autocommit SELECTs.
	query := "SELECT * FROM livecomments WHERE livestream_id = ? ORDER BY created_at DESC"
	if c.QueryParam("limit") != "" {
		limit, err := strconv.Atoi(c.QueryParam("limit"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
		}
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	livecommentModels := []LivecommentModel{}
	if err := dbConn.SelectContext(ctx, &livecommentModels, query, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomments: "+err.Error())
	}

	livecomments, err := fillLivecommentsForLivestream(ctx, dbConn, livecommentModels, int64(livestreamID))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fil livecomments: "+err.Error())
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
	if err := decodeRequestBody(c, &req); err != nil {
		return err
	}

	livecomment, err := postLivecommentWithDeadlockRetry(ctx, userID, livestreamID, req)
	if err != nil {
		return postLivecommentHandlerError(err)
	}
	return c.JSON(http.StatusCreated, livecomment)
}

var (
	errPostLivecommentBegin       = errors.New("post livecomment: begin")
	errPostLivecommentNotFound    = errors.New("post livecomment: livestream not found")
	errPostLivecommentGetLivestream = errors.New("post livecomment: get livestream")
	errPostLivecommentGetNGWords  = errors.New("post livecomment: get ng words")
	errPostLivecommentSpam        = errors.New("post livecomment: spam")
	errPostLivecommentInsert      = errors.New("post livecomment: insert")
	errPostLivecommentLastInsertID = errors.New("post livecomment: last insert id")
	errPostLivecommentFill        = errors.New("post livecomment: fill")
	errPostLivecommentCommit      = errors.New("post livecomment: commit")
)

func postLivecommentWithDeadlockRetry(ctx context.Context, userID int64, livestreamID int, req *PostLivecommentRequest) (Livecomment, error) {
	var committed postLivecommentCommitted
	err := withMySQLDeadlockRetry(func() error {
		livecommentModel, livestreamModel, err := postLivecommentInsertOnce(ctx, userID, livestreamID, req)
		if err != nil {
			return err
		}
		committed = postLivecommentCommitted{
			livecommentModel: livecommentModel,
			livestreamModel:  livestreamModel,
		}
		return nil
	})
	if err != nil {
		return Livecomment{}, err
	}

	livecomment, err := fillLivecommentResponseWithLivestream(ctx, dbConn, committed.livecommentModel, committed.livestreamModel)
	if err != nil {
		return Livecomment{}, fmt.Errorf("%w: %w", errPostLivecommentFill, err)
	}
	statsOnLivecommentPosted(committed.livestreamModel.UserID, int64(livestreamID), req.Tip)
	return livecomment, nil
}

type postLivecommentCommitted struct {
	livecommentModel LivecommentModel
	livestreamModel  LivestreamModel
}

func postLivecommentInsertOnce(ctx context.Context, userID int64, livestreamID int, req *PostLivecommentRequest) (LivecommentModel, LivestreamModel, error) {
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return LivecommentModel{}, LivestreamModel{}, fmt.Errorf("%w: %w", errPostLivecommentBegin, err)
	}
	defer tx.Rollback()

	livestreamModel, err := getLivestreamModelByID(ctx, tx, int64(livestreamID))
	if errors.Is(err, sql.ErrNoRows) {
		return LivecommentModel{}, LivestreamModel{}, errPostLivecommentNotFound
	}
	if err != nil {
		return LivecommentModel{}, LivestreamModel{}, fmt.Errorf("%w: %w", errPostLivecommentGetLivestream, err)
	}

	var ngwords []*NGWord
	if err := tx.SelectContext(ctx, &ngwords, "SELECT id, user_id, livestream_id, word FROM ng_words WHERE user_id = ? AND livestream_id = ?", livestreamModel.UserID, livestreamModel.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return LivecommentModel{}, LivestreamModel{}, fmt.Errorf("%w: %w", errPostLivecommentGetNGWords, err)
	}

	for _, ngword := range ngwords {
		if strings.Contains(req.Comment, ngword.Word) {
			return LivecommentModel{}, LivestreamModel{}, errPostLivecommentSpam
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
		return LivecommentModel{}, LivestreamModel{}, fmt.Errorf("%w: %w", errPostLivecommentInsert, err)
	}

	livecommentID, err := rs.LastInsertId()
	if err != nil {
		return LivecommentModel{}, LivestreamModel{}, fmt.Errorf("%w: %w", errPostLivecommentLastInsertID, err)
	}
	livecommentModel.ID = livecommentID

	if err := tx.Commit(); err != nil {
		return LivecommentModel{}, LivestreamModel{}, fmt.Errorf("%w: %w", errPostLivecommentCommit, err)
	}

	return livecommentModel, livestreamModel, nil
}

func postLivecommentHandlerError(err error) *echo.HTTPError {
	switch {
	case errors.Is(err, errPostLivecommentSpam):
		return echo.NewHTTPError(http.StatusBadRequest, "このコメントがスパム判定されました")
	case errors.Is(err, errPostLivecommentNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
	case errors.Is(err, errPostLivecommentBegin):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	case errors.Is(err, errPostLivecommentGetLivestream):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	case errors.Is(err, errPostLivecommentGetNGWords):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get NG words: "+err.Error())
	case errors.Is(err, errPostLivecommentInsert):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livecomment: "+err.Error())
	case errors.Is(err, errPostLivecommentLastInsertID):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted livecomment id: "+err.Error())
	case errors.Is(err, errPostLivecommentFill):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment: "+err.Error())
	case errors.Is(err, errPostLivecommentCommit):
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	default:
		if isMySQLDeadlock(err) {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livecomment: "+err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
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

	livestreamModel, err := getLivestreamModelByID(ctx, tx, int64(livestreamID))
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
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

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	// Response fill is read-only; run after commit so the write txn is not held during owner/tag SELECTs.
	report, err := fillLivecommentReportResponseWithLivestream(ctx, dbConn, reportModel, livecommentModel, livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment report: "+err.Error())
	}
	statsOnReportPosted(int64(livestreamID))

	return c.JSON(http.StatusCreated, report)
}

const spamLivecommentDeleteBatchSize = 500

// deleteLivecommentsHittingNgWords は moderate 用。当該配信の全 NG ワードにマッチするコメントを削除する。
// 単一の広い DELETE より、id 順にバッチで FOR UPDATE → PK 削除し、ロック順序と保持時間を抑える。
func deleteLivecommentsHittingNgWords(ctx context.Context, tx *sqlx.Tx, livestreamID int) (moderateDeleteStats, error) {
	const selectRows = `
		SELECT lc.id, lc.tip FROM livecomments lc
		WHERE lc.livestream_id = ?
		AND EXISTS (
			SELECT 1 FROM ng_words nw
			WHERE nw.livestream_id = ?
			AND lc.comment LIKE CONCAT('%', nw.word, '%')
		)
		ORDER BY lc.id
		LIMIT ?
		FOR UPDATE`

	var total moderateDeleteStats
	for {
		var rows []struct {
			ID  int64 `db:"id"`
			Tip int64 `db:"tip"`
		}
		if err := tx.SelectContext(ctx, &rows, selectRows, livestreamID, livestreamID, spamLivecommentDeleteBatchSize); err != nil {
			return total, err
		}
		if len(rows) == 0 {
			return total, nil
		}
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
			total.Count++
			total.TipSum += r.Tip
			if r.Tip > total.MaxDeleted {
				total.MaxDeleted = r.Tip
			}
		}

		query, args, err := sqlx.In("DELETE FROM livecomments WHERE id IN (?)", ids)
		if err != nil {
			return total, err
		}
		query = tx.Rebind(query)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return total, err
		}
	}
}

func moderateLivestream(ctx context.Context, userID int64, livestreamID int, ngWord string) (wordID int64, del moderateDeleteStats, err error) {
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return 0, del, fmt.Errorf("%w: %w", errModerateBegin, err)
	}
	defer tx.Rollback()

	owned, err := getLivestreamModelByID(ctx, tx, int64(livestreamID))
	if errors.Is(err, sql.ErrNoRows) {
		return 0, del, errModerateNotOwner
	}
	if err != nil {
		return 0, del, fmt.Errorf("%w: %w", errModerateGetLivestreams, err)
	}
	if owned.UserID != userID {
		return 0, del, errModerateNotOwner
	}

	rs, err := tx.NamedExecContext(ctx, "INSERT INTO ng_words(user_id, livestream_id, word, created_at) VALUES (:user_id, :livestream_id, :word, :created_at)", &NGWord{
		UserID:       userID,
		LivestreamID: int64(livestreamID),
		Word:         ngWord,
		CreatedAt:    time.Now().Unix(),
	})
	if err != nil {
		return 0, del, fmt.Errorf("%w: %w", errModerateInsertNGWord, err)
	}

	wordID, err = rs.LastInsertId()
	if err != nil {
		return 0, del, fmt.Errorf("%w: %w", errModerateLastInsertID, err)
	}

	del, err = deleteLivecommentsHittingNgWords(ctx, tx, livestreamID)
	if err != nil {
		return 0, del, fmt.Errorf("%w: %w", errModerateDeleteSpam, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, del, fmt.Errorf("%w: %w", errModerateCommit, err)
	}
	statsOnModerateDeleted(ctx, int64(livestreamID), owned.UserID, del)
	return wordID, del, nil
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
	if err := decodeRequestBody(c, &req); err != nil {
		return err
	}

	var wordID int64
	var lastErr error
	for attempt := 0; attempt < maxMySQLDeadlockRetries; attempt++ {
		wordID, _, lastErr = moderateLivestream(ctx, userID, livestreamID, req.NGWord)
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
	return moderateHandlerError(lastErr)
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
// fillLivestreamsResponseWithOwners 経由で2回目の owner 用 users/themes/icons SELECT を避ける。
func fillLivecommentsForLivestream(ctx context.Context, q readQueryer, livecommentModels []LivecommentModel, livestreamID int64) ([]Livecomment, error) {
	if len(livecommentModels) == 0 {
		return []Livecomment{}, nil
	}

	var livestreamPtr *Livestream
	if cached, ok := getLivestreamFromCache(ctx, livestreamID); ok {
		livestreamPtr = cached
	} else if assembled, ok := assembleLivestreamFromProcessCache(ctx, livestreamID, nil); ok {
		livestreamPtr = assembled
		cacheLivestream(ctx, livestreamID, livestreamPtr)
	} else {
		livestreamModel, err := getLivestreamModelByID(ctx, q, livestreamID)
		if err != nil {
			return nil, err
		}

		userIDs := make([]int64, 0, len(livecommentModels)+1)
		for _, m := range livecommentModels {
			userIDs = append(userIDs, m.UserID)
		}
		userIDs = append(userIDs, livestreamModel.UserID)
		userIDs = uniqueInt64(userIDs)

		usersByID, err := fillUsersByIDs(ctx, q, userIDs)
		if err != nil {
			return nil, err
		}

		livestreams, err := fillLivestreamsResponseWithOwners(ctx, q, []*LivestreamModel{&livestreamModel}, []int64{livestreamID}, usersByID)
		if err != nil {
			return nil, err
		}
		if len(livestreams) != 1 {
			return nil, sql.ErrNoRows
		}
		livestream := livestreams[0]
		if livestream.Tags == nil {
			livestream.Tags = []Tag{}
		}
		livestreamPtr = &livestream
	}

	userIDs := make([]int64, 0, len(livecommentModels))
	for _, m := range livecommentModels {
		userIDs = append(userIDs, m.UserID)
	}
	userIDs = uniqueInt64(userIDs)

	usersByID, err := fillUsersByIDs(ctx, q, userIDs)
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
			Livestream: livestreamPtr,
			Comment:    m.Comment,
			Tip:        m.Tip,
			CreatedAt:  m.CreatedAt,
		}
	}
	return livecomments, nil
}

// fillLivecommentResponseWithLivestream は POST コメント用。ハンドラが既に取得した livestream 行を
// fillLivestreamsResponseWithOwners に渡し、livestreams の再 SELECT と owner 用の2回目 fillUsersByIDs を避ける。
func fillLivecommentResponseWithLivestream(ctx context.Context, q readQueryer, livecommentModel LivecommentModel, livestreamModel LivestreamModel) (Livecomment, error) {
	if livecommentModel.LivestreamID != livestreamModel.ID {
		return Livecomment{}, fmt.Errorf("livestream not found: %d", livecommentModel.LivestreamID)
	}

	userIDs := uniqueInt64([]int64{livecommentModel.UserID, livestreamModel.UserID})
	usersByID, err := fillUsersByIDs(ctx, q, userIDs)
	if err != nil {
		return Livecomment{}, err
	}

	livestreams, err := fillLivestreamsResponseWithOwners(ctx, q, []*LivestreamModel{&livestreamModel}, []int64{livestreamModel.ID}, usersByID)
	if err != nil {
		return Livecomment{}, err
	}
	if len(livestreams) != 1 {
		return Livecomment{}, sql.ErrNoRows
	}
	livestream := livestreams[0]
	if livestream.Tags == nil {
		livestream.Tags = []Tag{}
	}
	livestreamPtr := &livestream

	user, ok := usersByID[livecommentModel.UserID]
	if !ok {
		return Livecomment{}, sql.ErrNoRows
	}

	return Livecomment{
		ID:         livecommentModel.ID,
		User:       user,
		Livestream: livestreamPtr,
		Comment:    livecommentModel.Comment,
		Tip:        livecommentModel.Tip,
		CreatedAt:  livecommentModel.CreatedAt,
	}, nil
}

// fillLivecommentReportsResponse は GET 通報一覧用。
// 通報者・コメント投稿者・配信者の user ID をまとめて fillUsersByIDs し、
// まとめ fill 経由の2回目の users/themes/icons SELECT を避ける。
func fillLivecommentReportsResponse(ctx context.Context, q readQueryer, reportModels []*LivecommentReportModel) ([]LivecommentReport, error) {
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

	livecommentRowModels := make([]LivecommentModel, 0, len(livecommentIDs))
	if err := selectIn(ctx, q, &livecommentRowModels, "SELECT * FROM livecomments WHERE id IN (?)", livecommentIDs); err != nil {
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

	livestreamRowModels, missLivestreamIDs := livestreamModelsFromProcessCache(livestreamIDs)
	for _, id := range missLivestreamIDs {
		model, err := coalescedLoadLivestreamRow(ctx, q, id)
		if err != nil {
			return nil, err
		}
		livestreamRowModels = append(livestreamRowModels, model)
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

	usersByID, err := fillUsersByIDs(ctx, q, userIDs)
	if err != nil {
		return nil, err
	}

	livestreamPtrs := make([]*LivestreamModel, len(livestreamRowModels))
	for i := range livestreamRowModels {
		livestreamPtrs[i] = &livestreamRowModels[i]
	}
	livestreams, err := fillLivestreamsResponseWithOwners(ctx, q, livestreamPtrs, livestreamIDs, usersByID)
	if err != nil {
		return nil, err
	}
	livestreamByID := make(map[int64]*Livestream, len(livestreams))
	for i := range livestreams {
		ls := &livestreams[i]
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
func fillLivecommentReportResponseWithLivestream(ctx context.Context, q readQueryer, reportModel LivecommentReportModel, livecommentModel LivecommentModel, livestreamModel LivestreamModel) (LivecommentReport, error) {
	if livecommentModel.LivestreamID != livestreamModel.ID {
		return LivecommentReport{}, fmt.Errorf("livestream not found: %d", livecommentModel.LivestreamID)
	}

	userIDs := uniqueInt64([]int64{reportModel.UserID, livecommentModel.UserID, livestreamModel.UserID})
	usersByID, err := fillUsersByIDs(ctx, q, userIDs)
	if err != nil {
		return LivecommentReport{}, err
	}

	livestreams, err := fillLivestreamsResponseWithOwners(ctx, q, []*LivestreamModel{&livestreamModel}, []int64{livestreamModel.ID}, usersByID)
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

	livestream := livestreams[0]
	if livestream.Tags == nil {
		livestream.Tags = []Tag{}
	}
	livestreamPtr := &livestream

	return LivecommentReport{
		ID:          reportModel.ID,
		Reporter:    reporter,
		Livecomment: Livecomment{
			ID:         livecommentModel.ID,
			User:       user,
			Livestream: livestreamPtr,
			Comment:    livecommentModel.Comment,
			Tip:        livecommentModel.Tip,
			CreatedAt:  livecommentModel.CreatedAt,
		},
		CreatedAt: reportModel.CreatedAt,
	}, nil
}
