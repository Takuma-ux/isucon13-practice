package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/jmoiron/sqlx"
	"golang.org/x/sync/singleflight"
)

var (
	livestreamRowLoadFlight singleflight.Group
	userProfileLoadFlight   singleflight.Group

	userProfileCoalescedFetchDelayHook func(userID int64) // tests: after DB read, before cache put

	processCacheTestLivestreamRowDBLoads atomic.Int64 // tests: count coalesced livestream row SELECTs
)

func coalescedLoadUserProfile(ctx context.Context, tx *sqlx.Tx, userID int64) (User, error) {
	if u, ok := getUserProfileFromProcessCache(userID); ok {
		return *u, nil
	}

	key := fmt.Sprintf("user:%d", userID)
	v, err, _ := userProfileLoadFlight.Do(key, func() (interface{}, error) {
		if u, ok := getUserProfileFromProcessCache(userID); ok {
			return *u, nil
		}

		fillEpoch := snapshotUserProfileCacheEpoch(userID)

		var um userFillModel
		if err := tx.GetContext(ctx, &um, "SELECT id, name, display_name, description FROM users WHERE id = ?", userID); err != nil {
			return nil, err
		}
		userModel := UserModel{
			ID:          um.ID,
			Name:        um.Name,
			DisplayName: um.DisplayName,
			Description: um.Description,
		}

		user, err := loadUserProfileFromDB(ctx, tx, userModel)
		if err != nil {
			return nil, err
		}

		if userProfileCoalescedFetchDelayHook != nil {
			userProfileCoalescedFetchDelayHook(userID)
		}

		putUserProfileCache(&user, fillEpoch)
		if cached, ok := getUserProfileFromProcessCache(userID); ok {
			return *cached, nil
		}
		return user, nil
	})
	if err != nil {
		return User{}, err
	}
	return v.(User), nil
}

func coalescedLoadLivestreamRow(ctx context.Context, tx *sqlx.Tx, livestreamID int64) (LivestreamModel, error) {
	if shell, ok := getLivestreamShellFromProcessCache(livestreamID); ok {
		return livestreamModelFromShell(shell), nil
	}

	key := fmt.Sprintf("ls:%d", livestreamID)
	v, err, _ := livestreamRowLoadFlight.Do(key, func() (interface{}, error) {
		if shell, ok := getLivestreamShellFromProcessCache(livestreamID); ok {
			return livestreamModelFromShell(shell), nil
		}

		processCacheTestLivestreamRowDBLoads.Add(1)

		var model LivestreamModel
		err := tx.GetContext(ctx, &model, "SELECT * FROM livestreams WHERE id = ?", livestreamID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		if err != nil {
			return nil, err
		}
		putLivestreamRowCache(&model)
		return model, nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LivestreamModel{}, sql.ErrNoRows
		}
		return LivestreamModel{}, err
	}
	return v.(LivestreamModel), nil
}
