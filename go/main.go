package main

// ISUCON的な参考: https://github.com/isucon/isucon12-qualify/blob/main/webapp/go/isuports.go#L336
// sqlx的な参考: https://jmoiron.github.io/sqlx/

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	echolog "github.com/labstack/gommon/log"
)

const (
	listenPort                     = 8080
	powerDNSSubdomainAddressEnvKey = "ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS"

	// Slowloris hardening; normal bench/API headers finish in milliseconds.
	httpReadHeaderTimeout = 10 * time.Second
	// Nginx client keepalive_timeout is 65s; upstream keepalive default is 60s — stay open slightly longer on the app side.
	httpIdleTimeout = 75 * time.Second
)

var (
	powerDNSSubdomainAddress string
	dbConn                   *sqlx.DB
	secret                   = []byte("isucon13_session_cookiestore_defaultsecret")
)

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	if secretKey, ok := os.LookupEnv("ISUCON13_SESSION_SECRETKEY"); ok {
		secret = []byte(secretKey)
	}
}

type InitializeResponse struct {
	Language string `json:"language"`
}

func connectDB(logger echo.Logger) (*sqlx.DB, error) {
	const (
		networkTypeEnvKey = "ISUCON13_MYSQL_DIALCONFIG_NET"
		addrEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_ADDRESS"
		portEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_PORT"
		userEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_USER"
		passwordEnvKey    = "ISUCON13_MYSQL_DIALCONFIG_PASSWORD"
		dbNameEnvKey      = "ISUCON13_MYSQL_DIALCONFIG_DATABASE"
		parseTimeEnvKey   = "ISUCON13_MYSQL_DIALCONFIG_PARSETIME"
	)

	conf := mysql.NewConfig()

	// 環境変数がセットされていなかった場合でも一旦動かせるように、デフォルト値を入れておく
	// この挙動を変更して、エラーを出すようにしてもいいかもしれない
	conf.Net = "tcp"
	conf.Addr = net.JoinHostPort("127.0.0.1", "3306")
	conf.User = "isucon"
	conf.Passwd = "isucon"
	conf.DBName = "isupipe"
	conf.ParseTime = true
	conf.InterpolateParams = true
	// Bounded I/O so a stalled MySQL cannot hold a handler goroutine forever (unix socket included).
	conf.Timeout = 5 * time.Second
	conf.ReadTimeout = 30 * time.Second
	conf.WriteTimeout = 30 * time.Second

	if v, ok := os.LookupEnv(networkTypeEnvKey); ok {
		conf.Net = v
	}
	if conf.Net == "unix" {
		if addr, ok := os.LookupEnv(addrEnvKey); ok {
			conf.Addr = addr
		} else {
			conf.Addr = "/var/run/mysqld/mysqld.sock"
		}
	} else if addr, ok := os.LookupEnv(addrEnvKey); ok {
		if port, ok2 := os.LookupEnv(portEnvKey); ok2 {
			conf.Addr = net.JoinHostPort(addr, port)
		} else {
			conf.Addr = net.JoinHostPort(addr, "3306")
		}
	}
	if v, ok := os.LookupEnv(userEnvKey); ok {
		conf.User = v
	}
	if v, ok := os.LookupEnv(passwordEnvKey); ok {
		conf.Passwd = v
	}
	if v, ok := os.LookupEnv(dbNameEnvKey); ok {
		conf.DBName = v
	}
	if v, ok := os.LookupEnv(parseTimeEnvKey); ok {
		parseTime, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("failed to parse environment variable '%s' as bool: %+v", parseTimeEnvKey, err)
		}
		conf.ParseTime = parseTime
	}

	db, err := sqlx.Open("mysql", conf.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(50)
	// Default MaxIdleConns is 2; with MaxOpenConns 50 that causes idle connections to be
	// closed and re-opened under concurrent load. Keep idle pool aligned with max open.
	db.SetMaxIdleConns(50)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}

func initializeHandler(c echo.Context) error {
	if out, err := runSubprocess(initSubprocessTimeout, "../sql/init.sh"); err != nil {
		c.Logger().Warnf("init.sh failed with err=%s", string(out))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to initialize: "+err.Error())
	}
	invalidateTagsCache()
	invalidateProcessReadCaches()
	if err := rebuildStatsFromDB(c.Request().Context()); err != nil {
		c.Logger().Warnf("stats rebuild after initialize failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to rebuild stats: "+err.Error())
	}
	// Tags: invalidateTagsCache above is sufficient. getCachedTags (GET /api/tag, search) reloads
	// from MySQL on first use — same path as warmTagsCache. Prewarm is optional (see main startup).

	c.Request().Header.Add("Content-Type", "application/json;charset=utf-8")
	return c.JSON(http.StatusOK, InitializeResponse{
		Language: "golang",
	})
}

func main() {
	// Local loadmix: GOGC=200 cut GC CPU ~8.5%→5.7% and raised ok/s ~46k→51k vs default 100.
	if _, ok := os.LookupEnv("GOGC"); !ok {
		debug.SetGCPercent(200)
	}

	e := echo.New()
	e.Debug = false
	e.Logger.SetLevel(echolog.INFO)
	e.JSONSerializer = GoJSONSerializer{}
	e.Use(middleware.Recover())
	if os.Getenv("ISUCON13_ACCESS_LOG") == "1" {
		e.Use(middleware.Logger())
	}
	gzipSkipIcon := func(c echo.Context) bool {
		// JPEG is already compressed; skipping saves CPU on a hot asset path.
		return strings.HasPrefix(c.Path(), "/api/user/") && strings.HasSuffix(c.Path(), "/icon")
	}
	switch os.Getenv("ISUCON13_GZIP") {
	case "1":
		e.Use(middleware.GzipWithConfig(middleware.GzipConfig{Skipper: gzipSkipIcon}))
	case "klauspost":
		e.Use(klauspostGzipMiddleware(klauspostGzipConfig{Skipper: gzipSkipIcon}))
	}
	cookieStore := sessions.NewCookieStore(secret)
	cookieStore.Options.Domain = "*.u.isucon.local"
	e.Use(session.Middleware(cookieStore))

	// 初期化
	e.POST("/api/initialize", initializeHandler)

	// top
	e.GET("/api/tag", getTagHandler)
	e.GET("/api/user/:username/theme", getStreamerThemeHandler)

	// livestream
	// reserve livestream
	e.POST("/api/livestream/reservation", reserveLivestreamHandler)
	// list livestream
	e.GET("/api/livestream/search", searchLivestreamsHandler)
	e.GET("/api/livestream", getMyLivestreamsHandler)
	e.GET("/api/user/:username/livestream", getUserLivestreamsHandler)
	// get livestream
	e.GET("/api/livestream/:livestream_id", getLivestreamHandler)
	// get polling livecomment timeline
	e.GET("/api/livestream/:livestream_id/livecomment", getLivecommentsHandler)
	// ライブコメント投稿
	e.POST("/api/livestream/:livestream_id/livecomment", postLivecommentHandler)
	e.POST("/api/livestream/:livestream_id/reaction", postReactionHandler)
	e.GET("/api/livestream/:livestream_id/reaction", getReactionsHandler)

	// (配信者向け)ライブコメントの報告一覧取得API
	e.GET("/api/livestream/:livestream_id/report", getLivecommentReportsHandler)
	e.GET("/api/livestream/:livestream_id/ngwords", getNgwords)
	// ライブコメント報告
	e.POST("/api/livestream/:livestream_id/livecomment/:livecomment_id/report", reportLivecommentHandler)
	// 配信者によるモデレーション (NGワード登録)
	e.POST("/api/livestream/:livestream_id/moderate", moderateHandler)

	// livestream_viewersにINSERTするため必要
	// ユーザ視聴開始 (viewer)
	e.POST("/api/livestream/:livestream_id/enter", enterLivestreamHandler)
	// ユーザ視聴終了 (viewer)
	e.DELETE("/api/livestream/:livestream_id/exit", exitLivestreamHandler)

	// user
	e.POST("/api/register", registerHandler)
	e.POST("/api/login", loginHandler)
	e.GET("/api/user/me", getMeHandler)
	// フロントエンドで、配信予約のコラボレーターを指定する際に必要
	e.GET("/api/user/:username", getUserHandler)
	e.GET("/api/user/:username/statistics", getUserStatisticsHandler)
	e.GET("/api/user/:username/icon", getIconHandler)
	e.POST("/api/icon", postIconHandler)

	// stats
	// ライブ配信統計情報
	e.GET("/api/livestream/:livestream_id/statistics", getLivestreamStatisticsHandler)

	// 課金情報
	e.GET("/api/payment", GetPaymentResult)

	e.HTTPErrorHandler = errorResponseHandler

	// DB接続
	conn, err := connectDB(e.Logger)
	if err != nil {
		e.Logger.Errorf("failed to connect db: %v", err)
		os.Exit(1)
	}
	defer conn.Close()
	dbConn = conn

	if err := rebuildStatsFromDB(context.Background()); err != nil {
		e.Logger.Warnf("stats rebuild on startup failed: %v", err)
	} else {
		e.Logger.Info("stats counters rebuilt from database")
	}

	if err := warmTagsCache(context.Background()); err != nil {
		e.Logger.Warnf("tags cache prewarm failed (first /api/tag will retry): %v", err)
	} else {
		e.Logger.Infof("tags cache prewarmed (%d tags)", len(tagsCache.Tags))
	}

	subdomainAddr, ok := os.LookupEnv(powerDNSSubdomainAddressEnvKey)
	if !ok {
		e.Logger.Errorf("environ %s must be provided", powerDNSSubdomainAddressEnvKey)
		os.Exit(1)
	}
	powerDNSSubdomainAddress = subdomainAddr

	if err := loadFallbackImage(); err != nil {
		e.Logger.Errorf("%v", err)
		os.Exit(1)
	}

	// HTTPサーバ起動
	listenAddr := net.JoinHostPort("", strconv.Itoa(listenPort))
	e.Server.ReadHeaderTimeout = httpReadHeaderTimeout
	e.Server.IdleTimeout = httpIdleTimeout
	// Default MaxHeaderBytes (1 MiB) is ample for session cookie + normal API headers.
	go func() {
		log.Println(http.ListenAndServe("127.0.0.1:6060", nil))
	}()
	if err := e.Start(listenAddr); err != nil {
		e.Logger.Errorf("failed to start HTTP server: %v", err)
		os.Exit(1)
	}
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func errorResponseHandler(err error, c echo.Context) {
	if he, ok := err.(*echo.HTTPError); ok {
		logHTTPError(c, he.Code, err)
		if e := c.JSON(he.Code, &ErrorResponse{Error: err.Error()}); e != nil {
			c.Logger().Errorf("%+v", e)
		}
		return
	}

	c.Logger().Errorf("error at %s: %+v", c.Path(), err)
	if e := c.JSON(http.StatusInternalServerError, &ErrorResponse{Error: err.Error()}); e != nil {
		c.Logger().Errorf("%+v", e)
	}
}

func logHTTPError(c echo.Context, code int, err error) {
	if code >= http.StatusInternalServerError {
		c.Logger().Errorf("error at %s: %+v", c.Path(), err)
		return
	}
	// Expected client rejections (validation, auth, spam filter, etc.) — avoid ERROR log I/O under bench load.
	c.Logger().Debugf("client error at %s (%d): %+v", c.Path(), code, err)
}
