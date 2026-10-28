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
	// tagsResolved is true after fill loaded livestream_tags; row-only cache entries leave this false.
	tagsResolved bool
}

type assembledLivestreamCacheEntry struct {
	livestream  *Livestream
	ownerUserID int64
	ownerEpoch  uint64 // snapshot of userProfileCacheEpoch[owner] at store time
}

var (
	livestreamShellCacheMu sync.RWMutex
	livestreamShellCache   = make(map[int64]*LivestreamShell)

	assembledLivestreamCacheMu    sync.RWMutex
	assembledLivestreamCache      = make(map[int64]*assembledLivestreamCacheEntry)
	assembledLivestreamCacheByOwner = make(map[int64]map[int64]struct{}) // owner user_id -> livestream ids

	userProfileCacheMu     sync.RWMutex
	userProfileCache       = make(map[int64]*User)
	userProfileCacheEpoch  = make(map[int64]uint64) // bumped on invalidate; fills snapshot before DB
	userProfileCachePutDelayHook func(userID int64) // tests only: runs before epoch check on put
)

// cloneTags returns a non-nil copy so zero-length tags JSON-encode as [] not null.
func cloneTags(tags []Tag) []Tag {
	if len(tags) == 0 {
		return []Tag{}
	}
	out := make([]Tag, len(tags))
	copy(out, tags)
	return out
}

func invalidateProcessReadCaches() {
	livestreamShellCacheMu.Lock()
	livestreamShellCache = make(map[int64]*LivestreamShell)
	livestreamShellCacheMu.Unlock()

	clearAssembledLivestreamCache()

	userProfileCacheMu.Lock()
	userProfileCache = make(map[int64]*User)
	userProfileCacheEpoch = make(map[int64]uint64)
	userProfileCacheMu.Unlock()

	clearIconImageCache()
	clearNGWordsCache()
	clearThemeCache()
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
	invalidateAssembledLivestreamCacheForOwner(userID)
}

func clearAssembledLivestreamCache() {
	assembledLivestreamCacheMu.Lock()
	assembledLivestreamCache = make(map[int64]*assembledLivestreamCacheEntry)
	assembledLivestreamCacheByOwner = make(map[int64]map[int64]struct{})
	assembledLivestreamCacheMu.Unlock()
}

func addAssembledLivestreamOwnerIndexLocked(ownerUserID, livestreamID int64) {
	if ownerUserID == 0 {
		return
	}
	set, ok := assembledLivestreamCacheByOwner[ownerUserID]
	if !ok {
		set = make(map[int64]struct{})
		assembledLivestreamCacheByOwner[ownerUserID] = set
	}
	set[livestreamID] = struct{}{}
}

func removeAssembledLivestreamOwnerIndexLocked(ownerUserID, livestreamID int64) {
	set := assembledLivestreamCacheByOwner[ownerUserID]
	if set == nil {
		return
	}
	delete(set, livestreamID)
	if len(set) == 0 {
		delete(assembledLivestreamCacheByOwner, ownerUserID)
	}
}

func invalidateAssembledLivestreamCacheForOwner(ownerUserID int64) {
	assembledLivestreamCacheMu.Lock()
	defer assembledLivestreamCacheMu.Unlock()
	for livestreamID := range assembledLivestreamCacheByOwner[ownerUserID] {
		delete(assembledLivestreamCache, livestreamID)
	}
	delete(assembledLivestreamCacheByOwner, ownerUserID)
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
	invalidateAssembledLivestreamCacheForOwner(user.ID)
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
	stored := &LivestreamShell{
		ID:           shell.ID,
		OwnerUserID:  shell.OwnerUserID,
		Title:        shell.Title,
		Description:  shell.Description,
		PlaylistUrl:  shell.PlaylistUrl,
		ThumbnailUrl: shell.ThumbnailUrl,
		Tags:         cloneTags(shell.Tags),
		StartAt:      shell.StartAt,
		EndAt:        shell.EndAt,
		tagsResolved: shell.tagsResolved,
	}
	livestreamShellCacheMu.Lock()
	livestreamShellCache[shell.ID] = stored
	livestreamShellCacheMu.Unlock()
}

func livestreamShellFromModel(model *LivestreamModel, tags []Tag, tagsResolved bool) *LivestreamShell {
	return &LivestreamShell{
		ID:           model.ID,
		OwnerUserID:  model.UserID,
		Title:        model.Title,
		Description:  model.Description,
		PlaylistUrl:  model.PlaylistUrl,
		ThumbnailUrl: model.ThumbnailUrl,
		Tags:         cloneTags(tags),
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

func getLivestreamModelByID(ctx context.Context, q readQueryer, livestreamID int64) (LivestreamModel, error) {
	return coalescedLoadLivestreamRow(ctx, q, livestreamID)
}

// ownerUserIDForLivestream returns the stream owner user ID, using the process shell when present.
func ownerUserIDForLivestream(ctx context.Context, q readQueryer, livestreamID int64) (int64, error) {
	if shell, ok := getLivestreamShellFromProcessCache(livestreamID); ok {
		return shell.OwnerUserID, nil
	}
	model, err := getLivestreamModelByID(ctx, q, livestreamID)
	if err != nil {
		return 0, err
	}
	return model.UserID, nil
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

func cloneLivestream(ls *Livestream) *Livestream {
	if ls == nil {
		return nil
	}
	cp := *ls
	if ls.Owner != nil {
		cp.Owner = cloneUser(ls.Owner)
	}
	cp.Tags = cloneTags(ls.Tags)
	return &cp
}

func putAssembledLivestreamCache(ls *Livestream) {
	if ls == nil || ls.Owner == nil {
		return
	}
	ownerID := ls.Owner.ID
	ownerEpoch := snapshotUserProfileCacheEpoch(ownerID)
	stored := cloneLivestream(ls)
	assembledLivestreamCacheMu.Lock()
	if prev := assembledLivestreamCache[ls.ID]; prev != nil && prev.ownerUserID != ownerID {
		removeAssembledLivestreamOwnerIndexLocked(prev.ownerUserID, ls.ID)
	}
	assembledLivestreamCache[ls.ID] = &assembledLivestreamCacheEntry{
		livestream:  stored,
		ownerUserID: ownerID,
		ownerEpoch:  ownerEpoch,
	}
	addAssembledLivestreamOwnerIndexLocked(ownerID, ls.ID)
	assembledLivestreamCacheMu.Unlock()
}

func getAssembledLivestreamFromProcessCache(ctx context.Context, livestreamID int64, owners map[int64]*User) (*Livestream, bool) {
	assembledLivestreamCacheMu.RLock()
	ent := assembledLivestreamCache[livestreamID]
	assembledLivestreamCacheMu.RUnlock()
	if ent != nil && ent.livestream != nil {
		if ent.ownerEpoch == snapshotUserProfileCacheEpoch(ent.ownerUserID) {
			return cloneLivestream(ent.livestream), true
		}
	}

	ls, ok := assembleLivestreamFromProcessCache(ctx, livestreamID, owners)
	if !ok {
		return nil, false
	}
	putAssembledLivestreamCache(ls)
	return ls, true
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
		Tags:         cloneTags(shell.Tags),
		StartAt:      shell.StartAt,
		EndAt:        shell.EndAt,
	}
	return ls, true
}
