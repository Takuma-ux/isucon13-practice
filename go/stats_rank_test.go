package main

import (
	"math/rand"
	"sort"
	"sync"
	"testing"
)

type statsMapsSnapshot struct {
	users       map[int64]*userStatCounters
	usernames   map[string]int64
	userNames   map[int64]string
	livestreams map[int64]*livestreamStatCounters
	liveOwners  map[int64]int64
	allUserIDs  []int64
	allLiveIDs  []int64
	maxTipLocks sync.Map
	paymentTips int64
}

func snapshotStatsMaps() statsMapsSnapshot {
	return statsMapsSnapshot{
		users:       statsUsers,
		usernames:   statsUsernames,
		userNames:   statsUserNames,
		livestreams: statsLivestreams,
		liveOwners:  statsLiveOwners,
		allUserIDs:  statsAllUserIDs,
		allLiveIDs:  statsAllLiveIDs,
		maxTipLocks: livestreamMaxTipLocks,
		paymentTips: statsPaymentTotalTips.Load(),
	}
}

func restoreStatsMaps(s statsMapsSnapshot) {
	statsUsers = s.users
	statsUsernames = s.usernames
	statsUserNames = s.userNames
	statsLivestreams = s.livestreams
	statsLiveOwners = s.liveOwners
	statsAllUserIDs = s.allUserIDs
	statsAllLiveIDs = s.allLiveIDs
	livestreamMaxTipLocks = s.maxTipLocks
	statsPaymentTotalTips.Store(s.paymentTips)
}

// rankUserViaFullSort is the legacy rank path (sort entire ranking each request).
func rankUserViaFullSort(entries []UserRankingEntry, username string) int64 {
	ranking := append(UserRanking(nil), entries...)
	sort.Sort(ranking)
	return userRankFromSorted(ranking, username)
}

func installUserRankingEntries(entries []UserRankingEntry) {
	statsUsers = make(map[int64]*userStatCounters, len(entries))
	statsUsernames = make(map[string]int64, len(entries))
	statsUserNames = make(map[int64]string, len(entries))
	statsAllUserIDs = make([]int64, 0, len(entries))
	for i, e := range entries {
		id := int64(i + 1)
		statsAllUserIDs = append(statsAllUserIDs, id)
		statsUsernames[e.Username] = id
		statsUserNames[id] = e.Username
		u := &userStatCounters{emoji: userEmojiHistogram{counts: make(map[string]int64)}}
		u.tips.Store(e.Score)
		statsUsers[id] = u
	}
}

func rankLivestreamViaFullSort(entries []LivestreamRankingEntry, livestreamID int64) int64 {
	ranking := append(LivestreamRanking(nil), entries...)
	sort.Sort(ranking)
	var rank int64 = 1
	for i := len(ranking) - 1; i >= 0; i-- {
		if ranking[i].LivestreamID == livestreamID {
			return rank
		}
		rank++
	}
	return 0
}

func installLivestreamRankingEntries(entries []LivestreamRankingEntry) {
	statsLivestreams = make(map[int64]*livestreamStatCounters, len(entries))
	statsAllLiveIDs = make([]int64, 0, len(entries))
	for _, e := range entries {
		statsAllLiveIDs = append(statsAllLiveIDs, e.LivestreamID)
		ls := &livestreamStatCounters{}
		ls.tips.Store(e.Score)
		statsLivestreams[e.LivestreamID] = ls
	}
}

func TestUserRankScoreCountMatchesFullSort(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for n := 0; n < 200; n++ {
		const users = 8
		entries := make([]UserRankingEntry, users)
		for i := 0; i < users; i++ {
			entries[i] = UserRankingEntry{
				Username: string(rune('a' + i)),
				Score:    rng.Int63n(20),
			}
		}

		statsRebuildMu.Lock()
		saved := snapshotStatsMaps()
		installUserRankingEntries(entries)
		for _, e := range entries {
			sortRank := rankUserViaFullSort(entries, e.Username)
			countRank := userRankFromScores(e.Username, e.Score)
			if sortRank != countRank {
				restoreStatsMaps(saved)
				statsRebuildMu.Unlock()
				t.Fatalf("user rank mismatch for %q: sort=%d production userRankFromScores=%d entries=%+v", e.Username, sortRank, countRank, entries)
			}
		}
		restoreStatsMaps(saved)
		statsRebuildMu.Unlock()
	}
}

func TestLivestreamRankScoreCountMatchesFullSort(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for n := 0; n < 200; n++ {
		const streams = 10
		entries := make([]LivestreamRankingEntry, streams)
		for i := 0; i < streams; i++ {
			entries[i] = LivestreamRankingEntry{
				LivestreamID: int64(i + 1),
				Score:        rng.Int63n(15),
			}
		}

		statsRebuildMu.Lock()
		saved := snapshotStatsMaps()
		installLivestreamRankingEntries(entries)
		for _, e := range entries {
			sortRank := rankLivestreamViaFullSort(entries, e.LivestreamID)
			countRank := livestreamRankFromScores(e.LivestreamID, e.Score)
			if sortRank != countRank {
				restoreStatsMaps(saved)
				statsRebuildMu.Unlock()
				t.Fatalf("livestream rank mismatch for id=%d: sort=%d production livestreamRankFromScores=%d entries=%+v", e.LivestreamID, sortRank, countRank, entries)
			}
		}
		restoreStatsMaps(saved)
		statsRebuildMu.Unlock()
	}
}
