package main

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/jmoiron/sqlx"
)

// SQL reference implementations for tests comparing memory stats to ground-truth queries.

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

func userStatisticsFromSQL(ctx context.Context, tx *sqlx.Tx, username string) (UserStatistics, error) {
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
		return UserStatistics{}, err
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
		return UserStatistics{}, errUserStatsNotFound
	}
	sort.Sort(ranking)
	rank := userRankFromSorted(ranking, username)

	var statsRow userStatsRow
	statsQuery := `
	SELECT
	  (SELECT COUNT(*) FROM livecomments lc INNER JOIN livestreams l ON l.id = lc.livestream_id WHERE l.user_id = ?) AS total_livecomments,
	  (SELECT IFNULL(SUM(lc.tip), 0) FROM livecomments lc INNER JOIN livestreams l ON l.id = lc.livestream_id WHERE l.user_id = ?) AS total_tip,
	  (SELECT COUNT(*) FROM livestream_viewers_history h INNER JOIN livestreams l ON l.id = h.livestream_id WHERE l.user_id = ?) AS viewers_count
	`
	if err := tx.GetContext(ctx, &statsRow, statsQuery, userID, userID, userID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return UserStatistics{}, err
	}
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
		return UserStatistics{}, err
	}
	return UserStatistics{
		Rank:              rank,
		ViewersCount:      statsRow.ViewersCount,
		TotalReactions:    totalReactions,
		TotalLivecomments: statsRow.TotalLivecomments,
		TotalTip:          statsRow.TotalTip,
		FavoriteEmoji:     favoriteEmoji,
	}, nil
}

func livestreamStatisticsFromSQL(ctx context.Context, tx *sqlx.Tx, livestreamID int64) (LivestreamStatistics, error) {
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
		return LivestreamStatistics{}, err
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
		return LivestreamStatistics{}, errLivestreamStatsNotFound
	}
	sort.Sort(ranking)
	var rank int64 = 1
	for i := len(ranking) - 1; i >= 0; i-- {
		if ranking[i].LivestreamID == livestreamID {
			break
		}
		rank++
	}
	var statsRow livestreamStatsRow
	statsQuery := `
	SELECT
	  (SELECT COUNT(*) FROM livestream_viewers_history WHERE livestream_id = ?) AS viewers_count,
	  (SELECT IFNULL(MAX(tip), 0) FROM livecomments WHERE livestream_id = ?) AS max_tip,
	  (SELECT COUNT(*) FROM reactions WHERE livestream_id = ?) AS total_reactions,
	  (SELECT COUNT(*) FROM livecomment_reports WHERE livestream_id = ?) AS total_reports
	`
	if err := tx.GetContext(ctx, &statsRow, statsQuery, livestreamID, livestreamID, livestreamID, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return LivestreamStatistics{}, err
	}
	return LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   statsRow.ViewersCount,
		MaxTip:         statsRow.MaxTip,
		TotalReactions: statsRow.TotalReactions,
		TotalReports:   statsRow.TotalReports,
	}, nil
}
