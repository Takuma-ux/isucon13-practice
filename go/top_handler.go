package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"
)

type Tag struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type TagModel struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

type TagsResponse struct {
	Tags []*Tag `json:"tags"`
}

var (
	tagsCacheMu   sync.RWMutex
	tagsCache     *TagsResponse
	tagsByIDCache map[int64]Tag // built with tagsCache; same lifetime (cleared on initialize)
)

// invalidateTagsCache clears the in-memory tag list (e.g. after POST /api/initialize).
func invalidateTagsCache() {
	tagsCacheMu.Lock()
	tagsCache = nil
	tagsByIDCache = nil
	tagsCacheMu.Unlock()
}

// warmTagsCache loads GET /api/tag data once (e.g. at process start).
func warmTagsCache(ctx context.Context) error {
	_, err := getCachedTags(ctx)
	return err
}

func loadTagsFromDB(ctx context.Context) (*TagsResponse, error) {
	var tagModels []*TagModel
	if err := dbConn.SelectContext(ctx, &tagModels, "SELECT * FROM tags"); err != nil {
		return nil, err
	}

	tags := make([]*Tag, len(tagModels))
	for i := range tagModels {
		tags[i] = &Tag{
			ID:   tagModels[i].ID,
			Name: tagModels[i].Name,
		}
	}
	return &TagsResponse{Tags: tags}, nil
}

// lookupTagIDsByName returns tag IDs whose name equals name (empty if none).
func lookupTagIDsByName(ctx context.Context, name string) ([]int, error) {
	tagsResponse, err := getCachedTags(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(tagsResponse.Tags))
	for _, tag := range tagsResponse.Tags {
		if tag.Name == name {
			ids = append(ids, int(tag.ID))
		}
	}
	return ids, nil
}

func buildTagsByIDMap(tagsResponse *TagsResponse) map[int64]Tag {
	tagByID := make(map[int64]Tag, len(tagsResponse.Tags))
	for _, tag := range tagsResponse.Tags {
		tagByID[tag.ID] = *tag
	}
	return tagByID
}

// tagByIDMapFromCache returns all tags keyed by id (same rows as SELECT * FROM tags WHERE id IN (...)).
func tagByIDMapFromCache(ctx context.Context) (map[int64]Tag, error) {
	if err := warmTagsByIDCache(ctx); err != nil {
		return nil, err
	}
	tagsCacheMu.RLock()
	tagByID := tagsByIDCache
	tagsCacheMu.RUnlock()
	return tagByID, nil
}

func warmTagsByIDCache(ctx context.Context) error {
	_, err := getCachedTags(ctx)
	return err
}

func getCachedTags(ctx context.Context) (*TagsResponse, error) {
	tagsCacheMu.RLock()
	if tagsCache != nil {
		cached := tagsCache
		tagsCacheMu.RUnlock()
		return cached, nil
	}
	tagsCacheMu.RUnlock()

	tagsCacheMu.Lock()
	defer tagsCacheMu.Unlock()
	if tagsCache != nil {
		return tagsCache, nil
	}
	loaded, err := loadTagsFromDB(ctx)
	if err != nil {
		return nil, err
	}
	tagsCache = loaded
	tagsByIDCache = buildTagsByIDMap(loaded)
	return tagsCache, nil
}

func getTagHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	tagsResponse, err := getCachedTags(ctx)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get tags: "+err.Error())
	}
	return c.JSON(http.StatusOK, tagsResponse)
}

// 配信者のテーマ取得API
// GET /api/user/:username/theme
func getStreamerThemeHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	if err := verifyUserSession(c); err != nil {
		return err
	}

	username := c.Param("username")

	userID, ok := statsLookupUserIDByName(username)
	if !ok {
		if err := dbConn.GetContext(ctx, &userID, "SELECT id FROM users WHERE name = ?", username); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
		}
	}

	themeModel, err := themeForUser(ctx, dbConn, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme: "+err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme: "+err.Error())
	}

	theme := Theme{
		ID:       themeModel.ID,
		DarkMode: themeModel.DarkMode,
	}

	return c.JSON(http.StatusOK, theme)
}
