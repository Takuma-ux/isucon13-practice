package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/sessions"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultSessionIDKey      = "SESSIONID" // cookie / session store name (not a sess.Values key)
	defaultSessionExpiresKey = "EXPIRES"
	defaultUserIDKey         = "USERID"
	bcryptDefaultCost        = bcrypt.MinCost
)

var fallbackImage = "../img/NoImage.jpg"

// fallbackImageBytes is NoImage.jpg loaded once at startup (see loadFallbackImage).
var fallbackImageBytes []byte

// SHA-256 (lowercase hex) of ../img/NoImage.jpg; matches ISUCON13 official commentary.
const defaultIconHash = "d9f8294e9d895f81ce62e73dc7d5dff862a4fa40bd4e0fecf53f7526a8edcac0"

func loadFallbackImage() error {
	b, err := os.ReadFile(fallbackImage)
	if err != nil {
		return fmt.Errorf("read fallback icon %s: %w", fallbackImage, err)
	}
	fallbackImageBytes = b
	return nil
}

func hashIconBytes(image []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(image))
}

// ifNoneMatchSatisfied reports whether If-None-Match precludes sending a response body (RFC 9110 §13.1.2).
// currentHash is the opaque-tag of the server's strong entity-tag (icon_hash hex).
func ifNoneMatchSatisfied(ifNoneMatchHeader, currentHash string) bool {
	tags := parseIfNoneMatchList(ifNoneMatchHeader)
	if len(tags) == 0 {
		return false
	}
	for _, tag := range tags {
		if tag.wildcard {
			return true
		}
		if entityTagMatchesForIfNoneMatch(tag, currentHash) {
			return true
		}
	}
	return false
}

type parsedEntityTag struct {
	wildcard bool
	weak     bool
	opaque   string
}

func skipOWS(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// parseIfNoneMatchList parses an If-None-Match field value (RFC 9110 §8.8.3).
func parseIfNoneMatchList(header string) []parsedEntityTag {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	if header == "*" {
		return []parsedEntityTag{{wildcard: true}}
	}

	var tags []parsedEntityTag
	i := 0
	for {
		i = skipOWS(header, i)
		if i >= len(header) {
			break
		}
		tag, ni, ok := parseIfNoneMatchEntityTag(header, i)
		if !ok {
			for ni = i + 1; ni < len(header) && header[ni] != ','; ni++ {
			}
			i = ni
		} else {
			tags = append(tags, tag)
			i = ni
		}
		i = skipOWS(header, i)
		if i >= len(header) {
			break
		}
		if header[i] != ',' {
			break
		}
		i++
	}
	return tags
}

func isEtagc(b byte) bool {
	if b == '"' {
		return false
	}
	if b == 0x21 {
		return true
	}
	if b >= 0x23 && b <= 0x7e {
		return true
	}
	return b >= 0x80 // obs-text
}

func opaqueTagCharsValid(opaque string) bool {
	for i := 0; i < len(opaque); i++ {
		if !isEtagc(opaque[i]) {
			return false
		}
	}
	return true
}

func parseIfNoneMatchEntityTag(s string, start int) (parsedEntityTag, int, bool) {
	i := skipOWS(s, start)
	if i >= len(s) {
		return parsedEntityTag{}, start, false
	}
	weak := false
	if strings.HasPrefix(s[i:], "W/") {
		weak = true
		i += 2
		i = skipOWS(s, i)
	}
	if i >= len(s) || s[i] != '"' {
		return parsedEntityTag{}, start, false
	}
	j := i + 1
	for j < len(s) && s[j] != '"' {
		j++
	}
	if j >= len(s) {
		return parsedEntityTag{}, start, false
	}
	opaque := s[i+1 : j]
	if !opaqueTagCharsValid(opaque) {
		return parsedEntityTag{}, start, false
	}
	after := skipOWS(s, j+1)
	if after < len(s) && s[after] != ',' {
		return parsedEntityTag{}, start, false
	}
	return parsedEntityTag{weak: weak, opaque: opaque}, j + 1, true
}

// entityTagMatchesForIfNoneMatch compares the client's validator to the current strong entity-tag (RFC 9110 §8.8.3.2).
func entityTagMatchesForIfNoneMatch(client parsedEntityTag, currentHash string) bool {
	server := parsedEntityTag{opaque: currentHash}
	return entityTagsWeakEquivalent(client, server)
}

func entityTagsWeakEquivalent(a, b parsedEntityTag) bool {
	// RFC 9110 §8.8.3.2: strong comparison requires both strong tags; weak comparison uses opaque-tag only.
	// Icon validators differ only by the weak prefix, so equality is opaque-tag equality in all cases here.
	return a.opaque == b.opaque
}

type UserModel struct {
	ID             int64  `db:"id"`
	Name           string `db:"name"`
	DisplayName    string `db:"display_name"`
	Description    string `db:"description"`
	HashedPassword string `db:"password"`
}

type User struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Theme       Theme  `json:"theme,omitempty"`
	IconHash    string `json:"icon_hash,omitempty"`
}

type Theme struct {
	ID       int64 `json:"id"`
	DarkMode bool  `json:"dark_mode"`
}

type ThemeModel struct {
	ID       int64 `db:"id"`
	UserID   int64 `db:"user_id"`
	DarkMode bool  `db:"dark_mode"`
}

type PostUserRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	// Password is non-hashed password.
	Password string               `json:"password"`
	Theme    PostUserRequestTheme `json:"theme"`
}

type PostUserRequestTheme struct {
	DarkMode bool `json:"dark_mode"`
}

type LoginRequest struct {
	Username string `json:"username"`
	// Password is non-hashed password.
	Password string `json:"password"`
}

type PostIconRequest struct {
	Image []byte `json:"image"`
}

type PostIconResponse struct {
	ID int64 `json:"id"`
}

func iconStrongETag(iconHash string) string {
	return `"` + iconHash + `"`
}

func iconHashForUserRow(stored string) string {
	if stored == "" {
		return defaultIconHash
	}
	return stored
}

func getIconHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	username := c.Param("username")
	ifNoneMatch := strings.TrimSpace(c.Request().Header.Get("If-None-Match"))

	const iconJoinFromUser = `
		SELECT COALESCE(i.icon_hash, '') AS icon_hash, i.image
		FROM users u
		LEFT JOIN icons i ON i.user_id = u.id
		WHERE u.name = ?`

	if ifNoneMatch != "" {
		var hashRow struct {
			IconHash string `db:"icon_hash"`
		}
		err := dbConn.GetContext(ctx, &hashRow, `
			SELECT COALESCE(i.icon_hash, '') AS icon_hash
			FROM users u
			LEFT JOIN icons i ON i.user_id = u.id
			WHERE u.name = ?`, username)
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
		}
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user icon: "+err.Error())
		}
		currentHash := iconHashForUserRow(hashRow.IconHash)
		if ifNoneMatchSatisfied(ifNoneMatch, currentHash) {
			return c.NoContent(http.StatusNotModified)
		}
	}

	var iconRow struct {
		IconHash string `db:"icon_hash"`
		Image    []byte `db:"image"`
	}
	if err := dbConn.GetContext(ctx, &iconRow, iconJoinFromUser, username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user icon: "+err.Error())
	}

	iconHash := iconHashForUserRow(iconRow.IconHash)
	c.Response().Header().Set("ETag", iconStrongETag(iconHash))
	c.Response().Header().Set("Cache-Control", "private, max-age=3600")
	if len(iconRow.Image) == 0 {
		return c.Blob(http.StatusOK, "image/jpeg", fallbackImageBytes)
	}
	return c.Blob(http.StatusOK, "image/jpeg", iconRow.Image)
}

func postIconHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *PostIconRequest
	if err := decodeRequestBody(c, &req); err != nil {
		return err
	}

	iconID, err := withMySQLDeadlockRetryInt64(func() (int64, error) {
		return postUserIcon(ctx, userID, req.Image)
	})
	if err != nil {
		if isMySQLDeadlock(err) {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert new user icon: "+err.Error())
		}
		if strings.Contains(err.Error(), "begin transaction") {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
		}
		if strings.Contains(err.Error(), "delete old user icon") {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete old user icon: "+err.Error())
		}
		if strings.Contains(err.Error(), "last inserted icon id") {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted icon id: "+err.Error())
		}
		if strings.Contains(err.Error(), "commit") {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert new user icon: "+err.Error())
	}

	return c.JSON(http.StatusCreated, &PostIconResponse{
		ID: iconID,
	})
}

func postUserIcon(ctx context.Context, userID int64, image []byte) (int64, error) {
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM icons WHERE user_id = ?", userID); err != nil {
		return 0, fmt.Errorf("failed to delete old user icon: %w", err)
	}

	iconHash := hashIconBytes(image)
	rs, err := tx.ExecContext(ctx, "INSERT INTO icons (user_id, image, icon_hash) VALUES (?, ?, ?)", userID, image, iconHash)
	if err != nil {
		return 0, err
	}

	iconID, err := rs.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last inserted icon id: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit: %w", err)
	}
	if err := writeThroughUserProfileCacheFromDB(ctx, userID); err != nil {
		return iconID, fmt.Errorf("refresh user profile cache: %w", err)
	}
	return iconID, nil
}

func loadUserProfileFromDB(ctx context.Context, tx *sqlx.Tx, userModel UserModel) (User, error) {
	themeModel := ThemeModel{}
	if err := tx.GetContext(ctx, &themeModel, "SELECT * FROM themes WHERE user_id = ?", userModel.ID); err != nil {
		return User{}, err
	}

	iconHash := defaultIconHash
	if err := tx.GetContext(ctx, &iconHash, "SELECT icon_hash FROM icons WHERE user_id = ?", userModel.ID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return User{}, err
		}
	}

	return User{
		ID:          userModel.ID,
		Name:        userModel.Name,
		DisplayName: userModel.DisplayName,
		Description: userModel.Description,
		Theme: Theme{
			ID:       themeModel.ID,
			DarkMode: themeModel.DarkMode,
		},
		IconHash: iconHash,
	}, nil
}

func writeThroughUserProfileCacheFromDB(ctx context.Context, userID int64) error {
	readTx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin read tx: %w", err)
	}
	defer readTx.Rollback()

	var userModel UserModel
	if err := readTx.GetContext(ctx, &userModel, "SELECT id, name, display_name, description FROM users WHERE id = ?", userID); err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	user, err := loadUserProfileFromDB(ctx, readTx, userModel)
	if err != nil {
		return err
	}
	if err := readTx.Commit(); err != nil {
		return fmt.Errorf("commit read tx: %w", err)
	}
	writeThroughUserProfileCache(&user)
	return nil
}

func getMeHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{}
	err = tx.GetContext(ctx, &userModel, "SELECT * FROM users WHERE id = ?", userID)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the userid in session")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	user, err := fillUserResponse(ctx, tx, userModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, user)
}

// ユーザ登録API
// POST /api/register
func registerHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	defer c.Request().Body.Close()

	req := PostUserRequest{}
	if err := decodeRequestBody(c, &req); err != nil {
		return err
	}

	if req.Name == "pipe" {
		return echo.NewHTTPError(http.StatusBadRequest, "the username 'pipe' is reserved")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptDefaultCost)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate hashed password: "+err.Error())
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{
		Name:           req.Name,
		DisplayName:    req.DisplayName,
		Description:    req.Description,
		HashedPassword: string(hashedPassword),
	}

	result, err := tx.NamedExecContext(ctx, "INSERT INTO users (name, display_name, description, password) VALUES(:name, :display_name, :description, :password)", userModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert user: "+err.Error())
	}

	userID, err := result.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted user id: "+err.Error())
	}

	userModel.ID = userID

	themeModel := ThemeModel{
		UserID:   userID,
		DarkMode: req.Theme.DarkMode,
	}
	if _, err := tx.NamedExecContext(ctx, "INSERT INTO themes (user_id, dark_mode) VALUES(:user_id, :dark_mode)", themeModel); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert user theme: "+err.Error())
	}

	if out, err := runSubprocess(pdnsutilSubprocessTimeout, "pdnsutil", "add-record", "u.isucon.local", req.Name, "A", "0", powerDNSSubdomainAddress); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, string(out)+": "+err.Error())
	}

	user, err := fillUserResponse(ctx, tx, userModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, user)
}

// ユーザログインAPI
// POST /api/login
func loginHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	defer c.Request().Body.Close()

	req := LoginRequest{}
	if err := decodeRequestBody(c, &req); err != nil {
		return err
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{}
	// usernameはUNIQUEなので、whereで一意に特定できる
	err = tx.GetContext(ctx, &userModel, "SELECT * FROM users WHERE name = ?", req.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid username or password")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	err = bcrypt.CompareHashAndPassword([]byte(userModel.HashedPassword), []byte(req.Password))
	if err == bcrypt.ErrMismatchedHashAndPassword {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid username or password")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to compare hash and password: "+err.Error())
	}

	sessionEndAt := time.Now().Add(1 * time.Hour)

	sess, err := session.Get(defaultSessionIDKey, c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}

	sess.Options = &sessions.Options{
		Domain: "u.isucon.local",
		MaxAge: int(60000),
		Path:   "/",
	}
	sess.Values[defaultUserIDKey] = userModel.ID
	sess.Values[defaultSessionExpiresKey] = sessionEndAt.Unix()

	if err := sess.Save(c.Request(), c.Response()); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save session: "+err.Error())
	}

	return c.NoContent(http.StatusOK)
}

// ユーザ詳細API
// GET /api/user/:username
func getUserHandler(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = withResponseCache(ctx)
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	username := c.Param("username")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{}
	if err := tx.GetContext(ctx, &userModel, "SELECT * FROM users WHERE name = ?", username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	user, err := fillUserResponse(ctx, tx, userModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, user)
}

func verifyUserSession(c echo.Context) error {
	sess, err := session.Get(defaultSessionIDKey, c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}

	sessionExpires, ok := sess.Values[defaultSessionExpiresKey]
	if !ok {
		return echo.NewHTTPError(http.StatusForbidden, "failed to get EXPIRES value from session")
	}

	_, ok = sess.Values[defaultUserIDKey].(int64)
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to get USERID value from session")
	}

	now := time.Now()
	if now.Unix() > sessionExpires.(int64) {
		return echo.NewHTTPError(http.StatusUnauthorized, "session has expired")
	}

	return nil
}

// responseCache holds per-request cached response objects to avoid
// redundant database reads and SHA-256 icon hashing for the same user
// or livestream appearing multiple times in a nested response.
type responseCache struct {
	users       map[int64]*User
	livestreams map[int64]*Livestream
}

type responseCacheKey struct{}

func withResponseCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, responseCacheKey{}, &responseCache{
		users:       make(map[int64]*User),
		livestreams: make(map[int64]*Livestream),
	})
}

func getUserFromCache(ctx context.Context, id int64) (*User, bool) {
	if v := ctx.Value(responseCacheKey{}); v != nil {
		if u, ok := v.(*responseCache).users[id]; ok {
			return u, true
		}
	}
	return nil, false
}

func cacheUser(ctx context.Context, id int64, user *User) {
	if v := ctx.Value(responseCacheKey{}); v != nil {
		v.(*responseCache).users[id] = user
	}
}

func getLivestreamFromCache(ctx context.Context, id int64) (*Livestream, bool) {
	if v := ctx.Value(responseCacheKey{}); v != nil {
		if l, ok := v.(*responseCache).livestreams[id]; ok {
			return l, true
		}
	}
	return nil, false
}

func cacheLivestream(ctx context.Context, id int64, livestream *Livestream) {
	if livestream != nil && livestream.Tags == nil {
		livestream.Tags = []Tag{}
	}
	if v := ctx.Value(responseCacheKey{}); v != nil {
		v.(*responseCache).livestreams[id] = livestream
	}
}

func fillUserResponse(ctx context.Context, tx *sqlx.Tx, userModel UserModel) (User, error) {
	if cached, ok := getUserFromCache(ctx, userModel.ID); ok {
		return *cached, nil
	}
	if profile, ok := getUserProfileFromProcessCache(userModel.ID); ok {
		cacheUser(ctx, userModel.ID, profile)
		return *profile, nil
	}

	user, err := coalescedLoadUserProfile(ctx, tx, userModel.ID)
	if err != nil {
		return User{}, err
	}
	cacheUser(ctx, userModel.ID, cloneUser(&user))
	return user, nil
}
