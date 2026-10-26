package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

type ReactionModel struct {
	ID           int64  `db:"id"`
	EmojiName    string `db:"emoji_name"`
	UserID       int64  `db:"user_id"`
	LivestreamID int64  `db:"livestream_id"`
	CreatedAt    int64  `db:"created_at"`
}

type Reaction struct {
	ID         int64       `json:"id"`
	EmojiName  string      `json:"emoji_name"`
	User       *User       `json:"user"`
	Livestream *Livestream `json:"livestream"`
	CreatedAt  int64      `json:"created_at"`
}

type PostReactionRequest struct {
	EmojiName string `json:"emoji_name"`
}

func getReactionsHandler(c echo.Context) error {
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

	query := "SELECT * FROM reactions WHERE livestream_id = ? ORDER BY created_at DESC"
	if c.QueryParam("limit") != "" {
		limit, err := strconv.Atoi(c.QueryParam("limit"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
		}
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	reactionModels := []ReactionModel{}
	if err := dbConn.SelectContext(ctx, &reactionModels, query, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "failed to get reactions")
	}

	reactions, err := fillReactionsForLivestream(ctx, dbConn, reactionModels, int64(livestreamID))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill reaction: "+err.Error())
	}

	return c.JSON(http.StatusOK, reactions)
}

func postReactionHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *PostReactionRequest
	if err := decodeRequestBody(c, &req); err != nil {
		return err
	}

	livestreamModel, err := getLivestreamModelByID(ctx, dbConn, int64(livestreamID))
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	reactionModel := ReactionModel{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		EmojiName:    req.EmojiName,
		CreatedAt:    time.Now().Unix(),
	}

	result, err := dbConn.NamedExecContext(ctx, "INSERT INTO reactions (user_id, livestream_id, emoji_name, created_at) VALUES (:user_id, :livestream_id, :emoji_name, :created_at)", reactionModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert reaction: "+err.Error())
	}

	reactionID, err := result.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted reaction id: "+err.Error())
	}
	reactionModel.ID = reactionID

	// Response fill is read-only; run after autocommit INSERT (same as postLivecomment / enter).
	reaction, err := fillReactionResponse(ctx, dbConn, reactionModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill reaction: "+err.Error())
	}
	statsOnReactionPosted(livestreamModel.UserID, int64(livestreamID), req.EmojiName)

	return c.JSON(http.StatusCreated, reaction)
}

// fillReactionsForLivestream は GET /api/livestream/:livestream_id/reaction 用。
// パスの livestream を1回取り、リアクション投稿者と配信者の user ID をまとめて fillUsersByIDs し、
// fillLivestreamsResponseWithOwners 経由で2回目の owner 用 users/themes/icons SELECT を避ける。
func fillReactionsForLivestream(ctx context.Context, q readQueryer, reactionModels []ReactionModel, livestreamID int64) ([]Reaction, error) {
	if len(reactionModels) == 0 {
		return []Reaction{}, nil
	}

	var err error
	var livestreamPtr *Livestream
	var usersByID map[int64]*User
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

		userIDs := make([]int64, 0, len(reactionModels)+1)
		for _, m := range reactionModels {
			userIDs = append(userIDs, m.UserID)
		}
		userIDs = append(userIDs, livestreamModel.UserID)
		userIDs = uniqueInt64(userIDs)

		usersByID, err = fillUsersByIDs(ctx, q, userIDs)
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

	if usersByID == nil {
		userIDs := make([]int64, 0, len(reactionModels))
		for _, m := range reactionModels {
			userIDs = append(userIDs, m.UserID)
		}
		userIDs = uniqueInt64(userIDs)

		usersByID, err = fillUsersByIDs(ctx, q, userIDs)
		if err != nil {
			return nil, err
		}
	}

	reactions := make([]Reaction, len(reactionModels))
	for i, m := range reactionModels {
		user, ok := usersByID[m.UserID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		reactions[i] = Reaction{
			ID:         m.ID,
			EmojiName:  m.EmojiName,
			User:       user,
			Livestream: livestreamPtr,
			CreatedAt:  m.CreatedAt,
		}
	}
	return reactions, nil
}

func fillReactionResponse(ctx context.Context, q readQueryer, reactionModel ReactionModel) (Reaction, error) {
	reactions, err := fillReactionsForLivestream(ctx, q, []ReactionModel{reactionModel}, reactionModel.LivestreamID)
	if err != nil {
		return Reaction{}, err
	}
	return reactions[0], nil
}
