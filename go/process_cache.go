package main

import (
	"context"

	"github.com/jmoiron/sqlx"
	"sync"
)

// LivestreamShell is immutable livestream payload without the embedded owner (owner resolved via userProfileCache).
type LivestreamShell struct {
	ID           int64
	OwnerUserID  int64
	Title        string
	Description  string
	PlaylistUrl  string
	ThumbnailUrl string
	Tags         []Tag
	StartAt      int64
	EndAt        int64
	// tagsResolved is true after fill loaded livestream_tags; row-only cache entries leave this false.
	tagsResolved bool
}

var (
	livestreamShellCacheMu sync.RWMutex
	livestreamShellCache   = make(map[int64]*LivestreamShell)

	userProfileCacheMu     sync.RWMutex
	userProfileCache       = make(map[int64]*User)
	userProfileCacheEpoch  = make(map[int64]uint64) // bumped on invalidate; fills snapshot before DB
	userProfileCachePutDelayHook func(userID int64) // tests only: runs before epoch check on put
)

func invalidateProcessReadCaches() {
	livestreamShellCacheMu.Lock()
	livestreamShellCache = make(map[int64]*LivestreamShell)
	livestreamShellCacheMu.Unlock()

	userProfileCacheMu.Lock()
	userProfileCache = make(map[int64]*User)
	userProfileCacheEpoch = make(map[int64]uint64)
	userProfileCacheMu.Unlock()
}

func snapshotUserProfileCacheEpoch(userID int64) uint64 {
	userProfileCacheMu.RLock()
	epoch := userProfileCacheEpoch[userID]
	userProfileCacheMu.RUnlock()
	return epoch
}

func invalidateUserProfileCache(userID int64) {
	userProfileCacheMu.Lock()
	delete(userProfileCache, userID)
	userProfileCacheEpoch[userID]++
	userProfileCacheMu.Unlock()
}

// writeThroughUserProfileCache bumps epoch (defeating in-flight stale fills) and stores the authoritative profile.
func writeThroughUserProfileCache(user *User) {
	if user == nil {
		return
	}
	stored := cloneUser(user)
	userProfileCacheMu.Lock()
	userProfileCacheEpoch[user.ID]++
	userProfileCache[user.ID] = stored
	userProfileCacheMu.Unlock()
}

func getLivestreamShellFromProcessCache(livestreamID int64) (*LivestreamShell, bool) {
	livestreamShellCacheMu.RLock()
	shell, ok := livestreamShellCache[livestreamID]
	livestreamShellCacheMu.RUnlock()
	if !ok || shell == nil {
		return nil, false
	}
	return shell, true
}

func putLivestreamShellCache(shell *LivestreamShell) {
	if shell == nil {
		return
	}
	tags := shell.Tags
	if tags == nil {
		tags = []Tag{}
	}
	stored := &LivestreamShell{
		ID:           shell.ID,
		OwnerUserID:  shell.OwnerUserID,
		Title:        shell.Title,
		Description:  shell.Description,
		PlaylistUrl:  shell.PlaylistUrl,
		ThumbnailUrl: shell.ThumbnailUrl,
		Tags:         append([]Tag(nil), tags...),
		StartAt:      shell.StartAt,
		EndAt:        shell.EndAt,
		tagsResolved: shell.tagsResolved,
	}
	livestreamShellCacheMu.Lock()
	livestreamShellCache[shell.ID] = stored
	livestreamShellCacheMu.Unlock()
}

func livestreamShellFromModel(model *LivestreamModel, tags []Tag, tagsResolved bool) *LivestreamShell {
	if tags == nil {
		tags = []Tag{}
	}
	return &LivestreamShell{
		ID:           model.ID,
		OwnerUserID:  model.UserID,
		Title:        model.Title,
		Description:  model.Description,
		PlaylistUrl:  model.PlaylistUrl,
		ThumbnailUrl: model.ThumbnailUrl,
		Tags:         append([]Tag(nil), tags...),
		StartAt:      model.StartAt,
		EndAt:        model.EndAt,
		tagsResolved: tagsResolved,
	}
}

func livestreamModelFromShell(shell *LivestreamShell) LivestreamModel {
	return LivestreamModel{
		ID:           shell.ID,
		UserID:       shell.OwnerUserID,
		Title:        shell.Title,
		Description:  shell.Description,
		PlaylistUrl:  shell.PlaylistUrl,
		ThumbnailUrl: shell.ThumbnailUrl,
		StartAt:      shell.StartAt,
		EndAt:        shell.EndAt,
	}
}

// putLivestreamRowCache stores immutable livestream row fields; does not downgrade a tagsResolved shell.
func putLivestreamRowCache(model *LivestreamModel) {
	if model == nil {
		return
	}
	livestreamShellCacheMu.Lock()
	defer livestreamShellCacheMu.Unlock()
	if ex, ok := livestreamShellCache[model.ID]; ok && ex != nil && ex.tagsResolved {
		return
	}
	shell := livestreamShellFromModel(model, []Tag{}, false)
	livestreamShellCache[model.ID] = shell
}

// getLivestreamModelByID returns the livestreams row, using process cache before SELECT.
func livestreamModelsFromProcessCache(livestreamIDs []int64) ([]LivestreamModel, []int64) {
	models := make([]LivestreamModel, 0, len(livestreamIDs))
	miss := make([]int64, 0, len(livestreamIDs))
	for _, id := range livestreamIDs {
		if shell, ok := getLivestreamShellFromProcessCache(id); ok {
			models = append(models, livestreamModelFromShell(shell))
			continue
		}
		miss = append(miss, id)
	}
	return models, miss
}

func getLivestreamModelByID(ctx context.Context, tx *sqlx.Tx, livestreamID int64) (LivestreamModel, error) {
	return coalescedLoadLivestreamRow(ctx, tx, livestreamID)
}

func getUserProfileFromProcessCache(userID int64) (*User, bool) {
	userProfileCacheMu.RLock()
	u, ok := userProfileCache[userID]
	userProfileCacheMu.RUnlock()
	if !ok || u == nil {
		return nil, false
	}
	return cloneUser(u), true
}

// putUserProfileCache stores user only if fillEpoch still matches (invalidation bumps epoch).
func putUserProfileCache(user *User, fillEpoch uint64) {
	putUserProfileCacheWithEpoch(user, fillEpoch)
}

func putUserProfileCacheWithEpoch(user *User, fillEpoch uint64) {
	if user == nil {
		return
	}
	if userProfileCachePutDelayHook != nil {
		userProfileCachePutDelayHook(user.ID)
	}
	stored := cloneUser(user)
	userProfileCacheMu.Lock()
	defer userProfileCacheMu.Unlock()
	if userProfileCacheEpoch[user.ID] != fillEpoch {
		return
	}
	userProfileCache[user.ID] = stored
}

func cloneUser(u *User) *User {
	if u == nil {
		return nil
	}
	cp := *u
	return &cp
}

// resolveOwnerUser returns owner for livestream assembly: owners map (current txn), then process user cache.
func resolveOwnerUser(ctx context.Context, ownerUserID int64, owners map[int64]*User) (*User, bool) {
	if owners != nil {
		if u, ok := owners[ownerUserID]; ok && u != nil {
			return cloneUser(u), true
		}
	}
	if u, ok := getUserProfileFromProcessCache(ownerUserID); ok {
		cacheUser(ctx, ownerUserID, u)
		return u, true
	}
	return nil, false
}

func assembleLivestreamFromProcessCache(ctx context.Context, livestreamID int64, owners map[int64]*User) (*Livestream, bool) {
	shell, ok := getLivestreamShellFromProcessCache(livestreamID)
	if !ok || !shell.tagsResolved {
		return nil, false
	}
	owner, ok := resolveOwnerUser(ctx, shell.OwnerUserID, owners)
	if !ok {
		return nil, false
	}
	ls := &Livestream{
		ID:           shell.ID,
		Owner:        owner,
		Title:        shell.Title,
		Description:  shell.Description,
		PlaylistUrl:  shell.PlaylistUrl,
		ThumbnailUrl: shell.ThumbnailUrl,
		Tags:         append([]Tag(nil), shell.Tags...),
		StartAt:      shell.StartAt,
		EndAt:        shell.EndAt,
	}
	return ls, true
}
