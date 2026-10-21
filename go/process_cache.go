package main

import (
	"context"
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
	}
	livestreamShellCacheMu.Lock()
	livestreamShellCache[shell.ID] = stored
	livestreamShellCacheMu.Unlock()
}

func livestreamShellFromModel(model *LivestreamModel, tags []Tag) *LivestreamShell {
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
	}
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
	if !ok {
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
