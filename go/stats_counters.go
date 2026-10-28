// Package main — stats_counters.go holds in-process aggregates for
// GET /api/user/:username/statistics and GET /api/livestream/:livestream_id/statistics.
//
// ╔══════════════════════════════════════════════════════════════════════════╗
// ║  SINGLE-PROCESS / SINGLE-INSTANCE ONLY                                   ║
// ║                                                                          ║
// ║  These counters live in this process's heap. They are NOT shared across    ║
// ║  multiple app servers, workers, or pods. A load-balanced deployment      ║
// ║  (e.g. nginx round-robin to 3 isupipe instances) will serve stale or     ║
// ║  inconsistent statistics unless every instance sees every write — which    ║
// ║  these counters do not provide.                                            ║
// ║                                                                          ║
// ║  Correct deployment for this design: exactly one isupipe process (one      ║
// ║  machine or one systemd unit) behind the bench / competition target.       ║
// ║                                                                          ║
// ║  Multi-instance stats would require a shared external store (Redis, etc.)  ║
// ║  or continuing to use MySQL for reads — explicitly out of scope here.      ║
// ╚══════════════════════════════════════════════════════════════════════════╝

package main

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
)

// statsLivestreamMaxTipFromDB, when set, replaces the MAX(tip) read (integration tests).
var statsLivestreamMaxTipFromDB func(ctx context.Context, livestreamID int64) (int64, error)

// statsModerateMaxTipRefreshFailures counts moderate-delete paths where counters were
// decremented but max_tip could not be refreshed from DB (visible partial state).
var statsModerateMaxTipRefreshFailures atomic.Uint64

func queryLivestreamMaxTipFromDB(ctx context.Context, livestreamID int64) (int64, error) {
	if statsLivestreamMaxTipFromDB != nil {
		return statsLivestreamMaxTipFromDB(ctx, livestreamID)
	}
	var dbMax int64
	err := dbConn.GetContext(ctx, &dbMax, "SELECT IFNULL(MAX(tip), 0) FROM livecomments WHERE livestream_id = ?", livestreamID)
	return dbMax, err
}

type userStatCounters struct {
	reactions atomic.Int64
	tips      atomic.Int64
	comments  atomic.Int64
	viewers   atomic.Int64
	emoji     userEmojiHistogram
}

type userEmojiHistogram struct {
	mu     sync.Mutex
	counts map[string]int64
}

type livestreamStatCounters struct {
	reactions atomic.Int64
	tips      atomic.Int64
	comments  atomic.Int64
	viewers   atomic.Int64
	reports   atomic.Int64
	maxTip    int64
}

var (
	statsRebuildMu sync.RWMutex

	statsUsers        = make(map[int64]*userStatCounters)
	statsUsernames    = make(map[string]int64) // name -> id
	statsUserNames    = make(map[int64]string) // id -> name
	statsLivestreams  = make(map[int64]*livestreamStatCounters)
	statsLiveOwners   = make(map[int64]int64) // livestream_id -> owner user_id
	statsAllUserIDs   []int64
	statsAllLiveIDs   []int64

	livestreamMaxTipLocks sync.Map // livestream_id -> *sync.Mutex

	// Global SUM(livecomments.tip) for GET /api/payment (single-process; updated with other stat hooks).
	statsPaymentTotalTips atomic.Int64
)

func livestreamMaxTipLock(livestreamID int64) *sync.Mutex {
	v, _ := livestreamMaxTipLocks.LoadOrStore(livestreamID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func ensureUserStats(userID int64) *userStatCounters {
	if u, ok := statsUsers[userID]; ok {
		return u
	}
	u := &userStatCounters{emoji: userEmojiHistogram{counts: make(map[string]int64)}}
	statsUsers[userID] = u
	return u
}

func ensureLivestreamStats(livestreamID int64) *livestreamStatCounters {
	if ls, ok := statsLivestreams[livestreamID]; ok {
		return ls
	}
	ls := &livestreamStatCounters{}
	statsLivestreams[livestreamID] = ls
	return ls
}

// statsRegisterLivestream adds a new livestream row to in-memory rank sets (zero counters).
func statsRegisterLivestream(livestreamID, ownerUserID int64) {
	statsRebuildMu.Lock()
	defer statsRebuildMu.Unlock()
	statsLiveOwners[livestreamID] = ownerUserID
	ensureLivestreamStats(livestreamID)
	statsAllLiveIDs = append(statsAllLiveIDs, livestreamID)
}

// statsOnUserRegistered registers a new user in rank/name maps (POST /api/register after commit).
// Mirrors the per-user loop in rebuildStatsFromDB; must run under statsRebuildMu like statsRegisterLivestream.
func statsOnUserRegistered(userID int64, username string) {
	statsRebuildMu.Lock()
	defer statsRebuildMu.Unlock()
	statsUsernames[username] = userID
	statsUserNames[userID] = username
	statsAllUserIDs = append(statsAllUserIDs, userID)
	ensureUserStats(userID)
}

func statsOnLivecommentPosted(ownerUserID, livestreamID int64, tip int64) {
	statsRebuildMu.RLock()
	defer statsRebuildMu.RUnlock()
	ls := ensureLivestreamStats(livestreamID)
	owner := ensureUserStats(ownerUserID)
	ls.comments.Add(1)
	ls.tips.Add(tip)
	owner.comments.Add(1)
	owner.tips.Add(tip)
	statsPaymentTotalTips.Add(tip)

	mu := livestreamMaxTipLock(livestreamID)
	mu.Lock()
	if tip > ls.maxTip {
		ls.maxTip = tip
	}
	mu.Unlock()
}

func statsOnReactionPosted(ownerUserID, livestreamID int64, emojiName string) {
	statsRebuildMu.RLock()
	defer statsRebuildMu.RUnlock()
	ls := ensureLivestreamStats(livestreamID)
	owner := ensureUserStats(ownerUserID)
	ls.reactions.Add(1)
	owner.reactions.Add(1)
	owner.emoji.mu.Lock()
	owner.emoji.counts[emojiName]++
	owner.emoji.mu.Unlock()
}

type moderateDeleteStats struct {
	Count      int
	TipSum     int64
	MaxDeleted int64
}

func statsOnModerateDeleted(ctx context.Context, livestreamID, ownerUserID int64, del moderateDeleteStats) {
	if del.Count == 0 {
		return
	}
	statsRebuildMu.RLock()
	defer statsRebuildMu.RUnlock()
	ls := ensureLivestreamStats(livestreamID)
	owner := ensureUserStats(ownerUserID)
	ls.comments.Add(-int64(del.Count))
	ls.tips.Add(-del.TipSum)
	owner.comments.Add(-int64(del.Count))
	owner.tips.Add(-del.TipSum)
	statsPaymentTotalTips.Add(-del.TipSum)

	mu := livestreamMaxTipLock(livestreamID)
	mu.Lock()
	defer mu.Unlock()
	if del.MaxDeleted < ls.maxTip {
		return
	}
	dbMax, err := withMySQLDeadlockRetryInt64(func() (int64, error) {
		return queryLivestreamMaxTipFromDB(ctx, livestreamID)
	})
	if err != nil {
		statsModerateMaxTipRefreshFailures.Add(1)
		log.Printf(
			"ERROR [stats] livestream %d: max_tip refresh failed after moderate delete: %v; comments/tips/payment already decremented, max_tip remains %d until rebuild or /api/initialize",
			livestreamID, err, ls.maxTip,
		)
		return
	}
	ls.maxTip = dbMax
}

func statsOnEnter(livestreamID int64) {
	statsRebuildMu.RLock()
	defer statsRebuildMu.RUnlock()
	owner, ok := statsLiveOwners[livestreamID]
	if !ok {
		return
	}
	ensureLivestreamStats(livestreamID).viewers.Add(1)
	ensureUserStats(owner).viewers.Add(1)
}

func statsOnExit(livestreamID int64, removed int64) {
	if removed <= 0 {
		return
	}
	statsRebuildMu.RLock()
	defer statsRebuildMu.RUnlock()
	owner, ok := statsLiveOwners[livestreamID]
	if !ok {
		return
	}
	ensureLivestreamStats(livestreamID).viewers.Add(-removed)
	ensureUserStats(owner).viewers.Add(-removed)
}

func statsOnReportPosted(livestreamID int64) {
	statsRebuildMu.RLock()
	defer statsRebuildMu.RUnlock()
	ensureLivestreamStats(livestreamID).reports.Add(1)
}

func userFavoriteEmojiLocked(u *userStatCounters) string {
	if u == nil {
		return ""
	}
	u.emoji.mu.Lock()
	defer u.emoji.mu.Unlock()
	var bestEmoji string
	var bestCount int64 = -1
	for emoji, cnt := range u.emoji.counts {
		if cnt > bestCount || (cnt == bestCount && emoji > bestEmoji) {
			bestCount = cnt
			bestEmoji = emoji
		}
	}
	return bestEmoji
}

// userRankFromScores counts users ranked above username using the same tie-break as UserRanking.Less.
// Caller must hold statsRebuildMu (at least RLock).
func userRankFromScores(username string, score int64) int64 {
	var above int64
	for _, id := range statsAllUserIDs {
		name := statsUserNames[id]
		if name == username {
			continue
		}
		var otherScore int64
		if u := statsUsers[id]; u != nil {
			otherScore = u.reactions.Load() + u.tips.Load()
		}
		if otherScore > score || (otherScore == score && name > username) {
			above++
		}
	}
	return above + 1
}

// livestreamRankFromScores counts streams ranked above livestreamID (tie-break: higher id wins on equal score).
// Caller must hold statsRebuildMu (at least RLock).
func livestreamRankFromScores(livestreamID int64, score int64) int64 {
	var above int64
	for _, id := range statsAllLiveIDs {
		if id == livestreamID {
			continue
		}
		var otherScore int64
		if ls := statsLivestreams[id]; ls != nil {
			otherScore = ls.reactions.Load() + ls.tips.Load()
		}
		if otherScore > score || (otherScore == score && id > livestreamID) {
			above++
		}
	}
	return above + 1
}

func userRankFromSorted(ranking UserRanking, username string) int64 {
	var rank int64 = 1
	for i := len(ranking) - 1; i >= 0; i-- {
		if ranking[i].Username == username {
			break
		}
		rank++
	}
	return rank
}

func userStatisticsFromMemory(username string) (UserStatistics, error) {
	statsRebuildMu.RLock()
	defer statsRebuildMu.RUnlock()
	userID, found := statsUsernames[username]
	if !found {
		return UserStatistics{}, errUserStatsNotFound
	}
	u := statsUsers[userID]
	var totalReactions int64
	var score int64
	if u != nil {
		totalReactions = u.reactions.Load()
		score = totalReactions + u.tips.Load()
	}
	rank := userRankFromScores(username, score)
	var viewers, comments, tips int64
	if u != nil {
		viewers = u.viewers.Load()
		comments = u.comments.Load()
		tips = u.tips.Load()
	}
	return UserStatistics{
		Rank:              rank,
		ViewersCount:      viewers,
		TotalReactions:    totalReactions,
		TotalLivecomments: comments,
		TotalTip:          tips,
		FavoriteEmoji:     userFavoriteEmojiLocked(u),
	}, nil
}

var errUserStatsNotFound = errors.New("user not found for statistics")

func livestreamStatisticsFromMemory(livestreamID int64) (LivestreamStatistics, error) {
	statsRebuildMu.RLock()
	if _, ok := statsLiveOwners[livestreamID]; !ok {
		statsRebuildMu.RUnlock()
		return LivestreamStatistics{}, errLivestreamStatsNotFound
	}
	ls := statsLivestreams[livestreamID]
	var score int64
	if ls != nil {
		score = ls.reactions.Load() + ls.tips.Load()
	}
	rank := livestreamRankFromScores(livestreamID, score)
	if ls == nil {
		ls = &livestreamStatCounters{}
	}
	viewers := ls.viewers.Load()
	reactions := ls.reactions.Load()
	reports := ls.reports.Load()
	statsRebuildMu.RUnlock()

	mu := livestreamMaxTipLock(livestreamID)
	mu.Lock()
	maxTip := ls.maxTip
	mu.Unlock()
	return LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   viewers,
		TotalReactions: reactions,
		TotalReports:   reports,
		MaxTip:         maxTip,
	}, nil
}

var errLivestreamStatsNotFound = errors.New("livestream not found for statistics")

// rebuildStatsFromDB replaces all in-memory stats from current MySQL contents.
func rebuildStatsFromDB(ctx context.Context) error {
	statsRebuildMu.Lock()
	defer statsRebuildMu.Unlock()

	statsUsers = make(map[int64]*userStatCounters)
	statsUsernames = make(map[string]int64)
	statsUserNames = make(map[int64]string)
	statsLivestreams = make(map[int64]*livestreamStatCounters)
	statsLiveOwners = make(map[int64]int64)
	statsAllUserIDs = nil
	statsAllLiveIDs = nil
	livestreamMaxTipLocks = sync.Map{}
	statsPaymentTotalTips.Store(0)

	var users []UserModel
	if err := dbConn.SelectContext(ctx, &users, "SELECT id, name FROM users ORDER BY id"); err != nil {
		return err
	}
	for _, u := range users {
		statsUsernames[u.Name] = u.ID
		statsUserNames[u.ID] = u.Name
		statsAllUserIDs = append(statsAllUserIDs, u.ID)
		ensureUserStats(u.ID)
	}

	var livestreams []LivestreamModel
	if err := dbConn.SelectContext(ctx, &livestreams, "SELECT id, user_id FROM livestreams ORDER BY id"); err != nil {
		return err
	}
	for _, ls := range livestreams {
		statsLiveOwners[ls.ID] = ls.UserID
		statsAllLiveIDs = append(statsAllLiveIDs, ls.ID)
		ensureLivestreamStats(ls.ID)
	}

	var reactionByUser []struct {
		UserID int64 `db:"user_id"`
		Cnt    int64 `db:"cnt"`
	}
	if err := dbConn.SelectContext(ctx, &reactionByUser, `
		SELECT l.user_id, COUNT(*) AS cnt
		FROM livestreams l INNER JOIN reactions r ON r.livestream_id = l.id
		GROUP BY l.user_id`); err != nil {
		return err
	}
	for _, row := range reactionByUser {
		ensureUserStats(row.UserID).reactions.Store(row.Cnt)
	}
	var reactionByLive []struct {
		LivestreamID int64 `db:"livestream_id"`
		Cnt          int64 `db:"cnt"`
	}
	if err := dbConn.SelectContext(ctx, &reactionByLive, `
		SELECT livestream_id, COUNT(*) AS cnt FROM reactions GROUP BY livestream_id`); err != nil {
		return err
	}
	for _, row := range reactionByLive {
		ensureLivestreamStats(row.LivestreamID).reactions.Store(row.Cnt)
	}

	var tipByUser []struct {
		UserID int64 `db:"user_id"`
		Sum    int64 `db:"tip_sum"`
	}
	if err := dbConn.SelectContext(ctx, &tipByUser, `
		SELECT l.user_id, IFNULL(SUM(lc.tip), 0) AS tip_sum
		FROM livestreams l INNER JOIN livecomments lc ON lc.livestream_id = l.id
		GROUP BY l.user_id`); err != nil {
		return err
	}
	for _, row := range tipByUser {
		ensureUserStats(row.UserID).tips.Store(row.Sum)
	}
	var commentByUser []struct {
		UserID int64 `db:"user_id"`
		Cnt    int64 `db:"cnt"`
	}
	if err := dbConn.SelectContext(ctx, &commentByUser, `
		SELECT l.user_id, COUNT(*) AS cnt
		FROM livestreams l INNER JOIN livecomments lc ON lc.livestream_id = l.id
		GROUP BY l.user_id`); err != nil {
		return err
	}
	for _, row := range commentByUser {
		ensureUserStats(row.UserID).comments.Store(row.Cnt)
	}

	var tipByLive []struct {
		LivestreamID int64 `db:"livestream_id"`
		Sum          int64 `db:"tip_sum"`
	}
	if err := dbConn.SelectContext(ctx, &tipByLive, `
		SELECT livestream_id, IFNULL(SUM(tip), 0) AS tip_sum FROM livecomments GROUP BY livestream_id`); err != nil {
		return err
	}
	var paymentTipTotal int64
	for _, row := range tipByLive {
		paymentTipTotal += row.Sum
		ensureLivestreamStats(row.LivestreamID).tips.Store(row.Sum)
	}
	statsPaymentTotalTips.Store(paymentTipTotal)
	var commentByLive []struct {
		LivestreamID int64 `db:"livestream_id"`
		Cnt          int64 `db:"cnt"`
	}
	if err := dbConn.SelectContext(ctx, &commentByLive, `
		SELECT livestream_id, COUNT(*) AS cnt FROM livecomments GROUP BY livestream_id`); err != nil {
		return err
	}
	for _, row := range commentByLive {
		ensureLivestreamStats(row.LivestreamID).comments.Store(row.Cnt)
	}

	var maxTips []struct {
		LivestreamID int64 `db:"livestream_id"`
		MaxTip       int64 `db:"max_tip"`
	}
	if err := dbConn.SelectContext(ctx, &maxTips, `
		SELECT livestream_id, IFNULL(MAX(tip), 0) AS max_tip FROM livecomments GROUP BY livestream_id`); err != nil {
		return err
	}
	for _, row := range maxTips {
		ensureLivestreamStats(row.LivestreamID).maxTip = row.MaxTip
	}

	var viewersByLive []struct {
		LivestreamID int64 `db:"livestream_id"`
		Cnt          int64 `db:"cnt"`
	}
	if err := dbConn.SelectContext(ctx, &viewersByLive, `
		SELECT livestream_id, COUNT(*) AS cnt FROM livestream_viewers_history GROUP BY livestream_id`); err != nil {
		return err
	}
	for _, row := range viewersByLive {
		ensureLivestreamStats(row.LivestreamID).viewers.Store(row.Cnt)
	}
	var viewersByUser []struct {
		UserID int64 `db:"user_id"`
		Cnt    int64 `db:"cnt"`
	}
	if err := dbConn.SelectContext(ctx, &viewersByUser, `
		SELECT l.user_id, COUNT(*) AS cnt
		FROM livestream_viewers_history h INNER JOIN livestreams l ON l.id = h.livestream_id
		GROUP BY l.user_id`); err != nil {
		return err
	}
	for _, row := range viewersByUser {
		ensureUserStats(row.UserID).viewers.Store(row.Cnt)
	}

	var reportsByLive []struct {
		LivestreamID int64 `db:"livestream_id"`
		Cnt          int64 `db:"cnt"`
	}
	if err := dbConn.SelectContext(ctx, &reportsByLive, `
		SELECT livestream_id, COUNT(*) AS cnt FROM livecomment_reports GROUP BY livestream_id`); err != nil {
		return err
	}
	for _, row := range reportsByLive {
		ensureLivestreamStats(row.LivestreamID).reports.Store(row.Cnt)
	}

	var emojiRows []struct {
		UserID    int64  `db:"user_id"`
		EmojiName string `db:"emoji_name"`
		Cnt       int64  `db:"cnt"`
	}
	if err := dbConn.SelectContext(ctx, &emojiRows, `
		SELECT l.user_id, r.emoji_name, COUNT(*) AS cnt
		FROM livestreams l
		INNER JOIN reactions r ON r.livestream_id = l.id
		GROUP BY l.user_id, r.emoji_name`); err != nil {
		return err
	}
	for _, row := range emojiRows {
		u := ensureUserStats(row.UserID)
		u.emoji.mu.Lock()
		u.emoji.counts[row.EmojiName] = row.Cnt
		u.emoji.mu.Unlock()
	}

	return nil
}
