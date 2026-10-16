package main

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/labstack/echo/v4"
)

type LivestreamStatistics struct {
	Rank           int64 `json:"rank"`
	ViewersCount   int64 `json:"viewers_count"`
	TotalReactions int64 `json:"total_reactions"`
	TotalReports   int64 `json:"total_reports"`
	MaxTip         int64 `json:"max_tip"`
}

type LivestreamRankingEntry struct {
	LivestreamID int64
	Score        int64
}
type LivestreamRanking []LivestreamRankingEntry

type livestreamScoreRow struct {
	ID             int64 `db:"id"`
	ReactionsCount int64 `db:"reactions_count"`
	TotalTips      int64 `db:"total_tips"`
}

type livestreamStatsRow struct {
	ViewersCount   int64 `db:"viewers_count"`
	MaxTip         int64 `db:"max_tip"`
	TotalReactions int64 `db:"total_reactions"`
	TotalReports   int64 `db:"total_reports"`
}

func (r LivestreamRanking) Len() int      { return len(r) }
func (r LivestreamRanking) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r LivestreamRanking) Less(i, j int) bool {
	if r[i].Score == r[j].Score {
		return r[i].LivestreamID < r[j].LivestreamID
	} else {
		return r[i].Score < r[j].Score
	}
}

type UserStatistics struct {
	Rank              int64  `json:"rank"`
	ViewersCount      int64  `json:"viewers_count"`
	TotalReactions    int64  `json:"total_reactions"`
	TotalLivecomments int64  `json:"total_livecomments"`
	TotalTip          int64  `json:"total_tip"`
	FavoriteEmoji     string `json:"favorite_emoji"`
}

type UserRankingEntry struct {
	Username string
	Score    int64
}
type UserRanking []UserRankingEntry

type userScoreRow struct {
	ID             int64  `db:"id"`
	Name           string `db:"name"`
	ReactionsCount int64  `db:"reactions_count"`
	TotalTips      int64  `db:"total_tips"`
}

type userStatsRow struct {
	TotalLivecomments int64 `db:"total_livecomments"`
	TotalTip          int64 `db:"total_tip"`
	ViewersCount      int64 `db:"viewers_count"`
}

func (r UserRanking) Len() int      { return len(r) }
func (r UserRanking) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r UserRanking) Less(i, j int) bool {
	if r[i].Score == r[j].Score {
		return r[i].Username < r[j].Username
	} else {
		return r[i].Score < r[j].Score
	}
}

func getUserStatisticsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	username := c.Param("username")
	// ユーザごとに、紐づく配信について、累計リアクション数、累計ライブコメント数、累計売上金額を算出
	// また、現在の合計視聴者数もだす

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// ランク算出: 全ユーザーのスコア(reaction数 + tip合計)を1クエリで取得
	var scores []userScoreRow
	scoreQuery := `
	SELECT u.id, u.name,
	       COALESCE(r.cnt, 0) AS reactions_count,
	       COALESCE(c.tip_sum, 0) AS total_tips
	FROM users u
	LEFT JOIN (
		SELECT l.user_id, COUNT(*) AS cnt
		FROM livestreams l
		INNER JOIN reactions r ON r.livestream_id = l.id
		GROUP BY l.user_id
	) r ON r.user_id = u.id
	LEFT JOIN (
		SELECT l.user_id, IFNULL(SUM(lc.tip), 0) AS tip_sum
		FROM livestreams l
		INNER JOIN livecomments lc ON lc.livestream_id = l.id
		GROUP BY l.user_id
	) c ON c.user_id = u.id
	`
	if err := tx.SelectContext(ctx, &scores, scoreQuery); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user scores: "+err.Error())
	}

	var ranking UserRanking
	var totalReactions int64
	var userID int64
	var userFound bool
	for _, s := range scores {
		ranking = append(ranking, UserRankingEntry{
			Username: s.Name,
			Score:    s.ReactionsCount + s.TotalTips,
		})
		if s.Name == username {
			userID = s.ID
			userFound = true
			totalReactions = s.ReactionsCount
		}
	}
	if !userFound {
		return echo.NewHTTPError(http.StatusBadRequest, "not found user that has the given username")
	}
	sort.Sort(ranking)

	var rank int64 = 1
	for i := len(ranking) - 1; i >= 0; i-- {
		entry := ranking[i]
		if entry.Username == username {
			break
		}
		rank++
	}

	// ライブコメント数、チップ合計、視聴者数を1クエリで取得
	var statsRow userStatsRow
	statsQuery := `
	SELECT
	  (SELECT COUNT(*) FROM livecomments lc INNER JOIN livestreams l ON l.id = lc.livestream_id WHERE l.user_id = ?) AS total_livecomments,
	  (SELECT IFNULL(SUM(lc.tip), 0) FROM livecomments lc INNER JOIN livestreams l ON l.id = lc.livestream_id WHERE l.user_id = ?) AS total_tip,
	  (SELECT COUNT(*) FROM livestream_viewers_history h INNER JOIN livestreams l ON l.id = h.livestream_id WHERE l.user_id = ?) AS viewers_count
	`
	if err := tx.GetContext(ctx, &statsRow, statsQuery, userID, userID, userID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user stats: "+err.Error())
	}

	// お気に入り絵文字
	var favoriteEmoji string
	query := `
	SELECT r.emoji_name
	FROM users u
	INNER JOIN livestreams l ON l.user_id = u.id
	INNER JOIN reactions r ON r.livestream_id = l.id
	WHERE u.name = ?
	GROUP BY emoji_name
	ORDER BY COUNT(*) DESC, emoji_name DESC
	LIMIT 1
	`
	if err := tx.GetContext(ctx, &favoriteEmoji, query, username); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find favorite emoji: "+err.Error())
	}

	stats := UserStatistics{
		Rank:              rank,
		ViewersCount:      statsRow.ViewersCount,
		TotalReactions:    totalReactions,
		TotalLivecomments: statsRow.TotalLivecomments,
		TotalTip:          statsRow.TotalTip,
		FavoriteEmoji:     favoriteEmoji,
	}
	return c.JSON(http.StatusOK, stats)
}

func getLivestreamStatisticsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	if err := verifyUserSession(c); err != nil {
		return err
	}

	id, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	livestreamID := int64(id)

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// ランク算出: 全ライブ配信のスコア(reaction数 + tip合計)を1クエリで取得
	var scores []livestreamScoreRow
	scoreQuery := `
	SELECT l.id,
	       COALESCE(r.cnt, 0) AS reactions_count,
	       COALESCE(c.tip_sum, 0) AS total_tips
	FROM livestreams l
	LEFT JOIN (
		SELECT livestream_id, COUNT(*) AS cnt FROM reactions GROUP BY livestream_id
	) r ON r.livestream_id = l.id
	LEFT JOIN (
		SELECT livestream_id, IFNULL(SUM(tip), 0) AS tip_sum FROM livecomments GROUP BY livestream_id
	) c ON c.livestream_id = l.id
	`
	if err := tx.SelectContext(ctx, &scores, scoreQuery); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream scores: "+err.Error())
	}

	var ranking LivestreamRanking
	var livestreamFound bool
	for _, s := range scores {
		ranking = append(ranking, LivestreamRankingEntry{
			LivestreamID: s.ID,
			Score:        s.ReactionsCount + s.TotalTips,
		})
		if s.ID == livestreamID {
			livestreamFound = true
		}
	}
	if !livestreamFound {
		return echo.NewHTTPError(http.StatusBadRequest, "cannot get stats of not found livestream")
	}
	sort.Sort(ranking)

	var rank int64 = 1
	for i := len(ranking) - 1; i >= 0; i-- {
		entry := ranking[i]
		if entry.LivestreamID == livestreamID {
			break
		}
		rank++
	}

	// 視聴者数、最大チップ額、リアクション数、スパム報告数を1クエリで取得
	var statsRow livestreamStatsRow
	statsQuery := `
	SELECT
	  (SELECT COUNT(*) FROM livestream_viewers_history WHERE livestream_id = ?) AS viewers_count,
	  (SELECT IFNULL(MAX(tip), 0) FROM livecomments WHERE livestream_id = ?) AS max_tip,
	  (SELECT COUNT(*) FROM reactions WHERE livestream_id = ?) AS total_reactions,
	  (SELECT COUNT(*) FROM livecomment_reports WHERE livestream_id = ?) AS total_reports
	`
	if err := tx.GetContext(ctx, &statsRow, statsQuery, livestreamID, livestreamID, livestreamID, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream stats: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   statsRow.ViewersCount,
		MaxTip:         statsRow.MaxTip,
		TotalReactions: statsRow.TotalReactions,
		TotalReports:   statsRow.TotalReports,
	})
}
