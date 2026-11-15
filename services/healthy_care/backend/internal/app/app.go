package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"healthycare/internal/config"
)

type App struct {
	DB     *pgxpool.Pool
	Cache  *redis.Client
	Config config.Config
	HTTP   *http.Client
}
type Actor struct {
	ID string
}

func New(ctx context.Context, cfg config.Config) (*App, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	poolConfig.MaxConns = 12
	db, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		db.Close()
		return nil, err
	}
	opts.DialTimeout = time.Second
	opts.ReadTimeout = time.Second
	opts.WriteTimeout = time.Second
	opts.MaxRetries = 0
	return &App{DB: db, Cache: redis.NewClient(opts), Config: cfg, HTTP: &http.Client{Timeout: 100 * time.Second, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 20, IdleConnTimeout: 60 * time.Second}}}, nil
}
func (a *App) Close()      { a.DB.Close(); _ = a.Cache.Close() }
func hash(v string) string { s := sha256.Sum256([]byte(v)); return hex.EncodeToString(s[:]) }
func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func actor(c *gin.Context) Actor { return c.MustGet("actor").(Actor) }
func fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": message}, "request_id": c.GetString("request_id")})
}
func internalError(c *gin.Context, err error) {
	attrs := []any{"request_id", c.GetString("request_id"), "route", c.FullPath(), "type", fmt.Sprintf("%T", err)}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		attrs = append(attrs, "sqlstate", databaseError.Code)
	}
	// Database errors can include rejected values from private health records.
	slog.Error("health request failed", attrs...)
	fail(c, 500, "internal_error", "暂时无法处理，请稍后重试。")
}
func bind(c *gin.Context, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(c, 400, "invalid_request", "请求格式或字段不正确。")
		return false
	}
	var extra any
	if dec.Decode(&extra) == io.EOF {
		return true
	}
	fail(c, 400, "invalid_request", "只允许一个JSON对象。")
	return false
}
func validID(c *gin.Context, name string) bool {
	if _, err := uuid.Parse(c.Param(name)); err != nil {
		fail(c, 400, "invalid_id", "记录编号不正确。")
		return false
	}
	return true
}
func equal(a, b string) bool {
	return len(a) > 0 && len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (a *App) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.RecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		slog.Error("request panic", "request_id", c.GetString("request_id"))
		fail(c, 500, "internal_error", "暂时无法处理，请稍后重试。")
	}))
	r.Use(func(c *gin.Context) {
		id := uuid.NewString()
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "no-store")
		c.Header("Referrer-Policy", "same-origin")
		c.Next()
	})
	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "alive"}) })
	r.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c, 2*time.Second)
		defer cancel()
		if err := a.DB.Ping(ctx); err != nil {
			fail(c, 503, "database_unavailable", "数据库尚未就绪。")
			return
		}
		c.JSON(200, gin.H{"status": "ready"})
	})
	u := r.Group("/v1/health")
	u.Use(a.authenticateCompanion)
	u.POST("/connect", a.connect)
	u.GET("/system/status", a.systemStatus)
	u.GET("/state", a.healthState)
	u.GET("/daily-summary", a.dailySummary)
	u.GET("/signals", a.historySignals)
	u.GET("/products", a.products)
	u.GET("/conversation", a.conversation)
	u.POST("/conversations/:id/messages", a.sendMessage)
	u.GET("/runs/:id", a.getRun)
	u.GET("/runs/:id/events", a.streamRun)
	u.POST("/runs/:id/cancel", a.cancelRun)
	u.POST("/runs/:id/retry", a.retryRun)
	u.POST("/actions/:id/confirm", a.confirmAction)
	u.POST("/actions/:id/reject", a.rejectAction)
	u.POST("/attachments", a.uploadAttachment)
	u.GET("/attachments/:id", a.getAttachment)
	u.GET("/export", a.personalExport)
	u.POST("/records/clear", a.clearRecords)
	i := r.Group("/internal")
	i.Use(func(c *gin.Context) {
		if !equal(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), a.Config.InternalToken) {
			fail(c, 401, "unauthorized", "未授权。")
			return
		}
		c.Next()
	})
	i.GET("/runs/:id/context", a.runContext)
	i.GET("/runs/:id/attachments/:attachment", a.runAttachment)
	r.NoRoute(func(c *gin.Context) { fail(c, 404, "not_found", "没有找到对应资源。") })
	return r
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func rows(ctx context.Context, q querier, sql string, args ...any) ([]map[string]any, error) {
	r, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []map[string]any{}
	for r.Next() {
		vals, e := r.Values()
		if e != nil {
			return nil, e
		}
		m := map[string]any{}
		for i, f := range r.FieldDescriptions() {
			v := vals[i]
			if id, ok := v.([16]byte); ok {
				v = uuid.UUID(id).String()
			}
			m[string(f.Name)] = v
		}
		out = append(out, m)
	}
	return out, r.Err()
}
func (a *App) audit(ctx context.Context, user, action, resource, purpose string) {
	if _, err := a.DB.Exec(ctx, "INSERT INTO audit_events(actor_id,action,resource_id,purpose) VALUES($1,$2,$3,$4)", user, action, resource, purpose); err != nil {
		slog.Error("audit failed", "error", err)
	}
}
