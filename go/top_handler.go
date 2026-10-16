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
	tagsCacheMu sync.RWMutex
	tagsCache   *TagsResponse
)

// invalidateTagsCache clears the in-memory tag list (e.g. after POST /api/initialize).
func invalidateTagsCache() {
	tagsCacheMu.Lock()
	tagsCache = nil
	tagsCacheMu.Unlock()
}

func loadTagsFromDB(ctx context.Context) (*TagsResponse, error) {
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var tagModels []*TagModel
	if err := tx.SelectContext(ctx, &tagModels, "SELECT * FROM tags"); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
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
		// echo.NewHTTPErrorが返っているのでそのまま出力
		c.Logger().Printf("verifyUserSession: %+v\n", err)
		return err
	}

	username := c.Param("username")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{}
	err = tx.GetContext(ctx, &userModel, "SELECT id FROM users WHERE name = ?", username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	themeModel := ThemeModel{}
	if err := tx.GetContext(ctx, &themeModel, "SELECT * FROM themes WHERE user_id = ?", userModel.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	theme := Theme{
		ID:       themeModel.ID,
		DarkMode: themeModel.DarkMode,
	}

	return c.JSON(http.StatusOK, theme)
}
