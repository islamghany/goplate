package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/islamghany/goplate/internal/auth"
	"github.com/islamghany/goplate/internal/auth/authpg"
	"github.com/islamghany/goplate/internal/cache"
	"github.com/islamghany/goplate/internal/config"
	"github.com/islamghany/goplate/internal/lifecycle"
	"github.com/islamghany/goplate/internal/note"
	"github.com/islamghany/goplate/internal/note/notepg"
	"github.com/islamghany/goplate/internal/platform/httpserver"
	"github.com/islamghany/goplate/internal/platform/jwt"
	"github.com/islamghany/goplate/internal/platform/logging"
	"github.com/islamghany/goplate/internal/platform/memory"
	"github.com/islamghany/goplate/internal/platform/migrate"
	"github.com/islamghany/goplate/internal/platform/otel"
	"github.com/islamghany/goplate/internal/platform/password"
	"github.com/islamghany/goplate/internal/platform/postgres"
	"github.com/islamghany/goplate/internal/platform/redis"
	"github.com/islamghany/goplate/internal/platform/riverjobs"
	"github.com/islamghany/goplate/internal/ratelimit"
	"github.com/islamghany/goplate/internal/user"
	"github.com/islamghany/goplate/internal/user/userpg"
	"github.com/islamghany/goplate/internal/web"
)

// build assembles the application. The order below is the startup order;
// the lifecycle manager stops components in reverse (ADR-0012).
func build(ctx context.Context, stdout io.Writer, environ map[string]string) (*lifecycle.Manager, error) {
	// 1. Configuration: fail fast, listing every problem at once.
	cfg, err := config.Load(environ)
	if err != nil {
		return nil, err
	}

	// 2. Logging and telemetry come first so everything after is observable.
	log := logging.New(stdout, logging.Options{
		Level:   cfg.Log.Level,
		Format:  cfg.Log.Format,
		Service: cfg.App.Name,
		Version: version,
		Extractors: []logging.Extractor{func(ctx context.Context) []slog.Attr {
			if id := web.RequestID(ctx); id != "" {
				return []slog.Attr{slog.String("request_id", id)}
			}
			return nil
		}},
	})
	log.InfoContext(ctx, "starting", "env", cfg.App.Env, "commit", commit)

	mgr := lifecycle.New(log, lifecycle.Config{DrainDelay: cfg.Shutdown.DrainDelay, ShutdownTimeout: cfg.Shutdown.Timeout})

	tel, err := otel.Setup(ctx, otel.Config{ServiceName: cfg.App.Name, ServiceVersion: version, Environment: cfg.App.Env}, log)
	if err != nil {
		return nil, err
	}
	mgr.Add(tel) // registered first => stopped last, so final spans are flushed

	// 3. Infrastructure. Connections are verified now, before any server
	//    listens, so a bad DSN fails startup with a clear error.
	pool, err := postgres.Open(ctx, postgres.Config{
		URL:             cfg.Postgres.URL.Value(),
		MaxConns:        cfg.Postgres.MaxConns,
		MinConns:        cfg.Postgres.MinConns,
		MaxConnLifetime: cfg.Postgres.MaxConnLifetime,
		MaxConnIdleTime: cfg.Postgres.MaxConnIdleTime,
	})
	if err != nil {
		return nil, err
	}
	mgr.Add(lifecycle.Func("postgres", nil, func(context.Context) error { pool.Close(); return nil }))
	mgr.AddChecker(lifecycle.CheckFunc("postgres", pool.Ping))

	if cfg.Postgres.MigrateOnStart {
		if err := migrate.Up(ctx, pool, log); err != nil {
			return nil, err
		}
	}
	txm := postgres.NewTxManager(pool)

	var rdb *goredis.Client
	if cfg.UsesRedis() {
		if rdb, err = redis.Open(ctx, cfg.Redis.URL.Value()); err != nil {
			return nil, err
		}
		mgr.Add(lifecycle.Func("redis", nil, func(context.Context) error { return rdb.Close() }))
		mgr.AddChecker(lifecycle.CheckFunc("redis", func(ctx context.Context) error { return rdb.Ping(ctx).Err() }))
	}

	var cacheStore cache.Store = memory.NewCache()
	if cfg.Cache.Driver == "redis" {
		cacheStore = redis.NewCache(rdb, log, cfg.App.Name+":")
	}
	var limiter ratelimit.Limiter = memory.NewLimiter()
	if cfg.RateLimit.Driver == "redis" {
		limiter = redis.NewLimiter(rdb, cfg.App.Name+":")
	}

	hasher := password.NewHasher(password.DefaultParams)
	signer, err := newSigner(cfg.Auth)
	if err != nil {
		return nil, err
	}

	// 4. The job queue is built in two phases: modules need it as their
	//    enqueuer before their handlers can be registered.
	queue := riverjobs.New(pool, riverjobs.Config{WorkerEnabled: cfg.Jobs.WorkerEnabled, MaxWorkers: cfg.Jobs.MaxWorkers}, log)

	// 5. Modules receive ports, never config.Config or concrete infrastructure.
	users := user.NewModule(user.Deps{Repo: userpg.NewRepository(txm), Hasher: hasher})

	var denylist auth.Denylist
	if cfg.Auth.DenylistEnabled {
		denylist = auth.NewCacheDenylist(redis.NewCache(rdb, log, cfg.App.Name+":"))
	}
	authn, err := auth.NewModule(auth.Deps{
		Users:    authUsers{users: users.Service}, // bridge: auth never imports user
		Sessions: authpg.NewSessionStore(txm),
		Tokens:   signer,
		Hasher:   hasher,
		Tx:       txm,
		Denylist: denylist,
		Throttle: &auth.LoginThrottle{
			Limiter:    limiter,
			Policy:     ratelimit.Policy{Name: "login-email", Rate: cfg.RateLimit.LoginEmailRate, Period: time.Hour, Burst: cfg.RateLimit.LoginEmailRate},
			FailClosed: cfg.RateLimit.LoginFailClosed,
		},
		Log: log,
	}, auth.ModuleConfig{
		Tokens: auth.Config{AccessTTL: cfg.Auth.AccessTTL, RefreshTTL: cfg.Auth.RefreshTTL},
		Transport: auth.TransportConfig{
			BearerEnabled: cfg.Auth.BearerEnabled,
			CookieEnabled: cfg.Auth.CookieEnabled,
			CookieSecure:  !cfg.IsDevelopment(),
			CookieDomain:  cfg.Auth.CookieDomain,
			RefreshPath:   "/v1/auth/refresh",
		},
	})
	if err != nil {
		return nil, err
	}

	notes := note.NewModule(note.Deps{Repo: notepg.NewRepository(txm), Tx: txm, Jobs: queue, Cache: cacheStore, Log: log},
		note.Config{CacheTTL: cfg.Note.CacheTTL})

	// 6. Background jobs: register handlers, then freeze the queue.
	riverjobs.Register(queue, notes.Service.HandleCreated)
	riverjobs.Register(queue, authn.Service.PurgeExpiredSessions)
	queue.Every(time.Hour, auth.PurgeExpiredSessions{})
	if err := queue.Build(); err != nil {
		return nil, err
	}
	mgr.Add(queue)

	// 7. HTTP servers. The public one is registered last so it is stopped
	//    first: stop taking requests before stopping what serves them.
	handler, err := newPublicHandler(cfg, log, mgr, limiter, modules{users: users, auth: authn, notes: notes})
	if err != nil {
		return nil, err
	}
	timeouts := httpserver.Timeouts{
		ReadHeader: cfg.HTTP.ReadHeaderTimeout,
		Read:       cfg.HTTP.ReadTimeout,
		Write:      cfg.HTTP.WriteTimeout,
		Idle:       cfg.HTTP.IdleTimeout,
	}
	mgr.Add(
		httpserver.New("admin-http", cfg.Admin.Addr, newAdminHandler(cfg, mgr, tel), timeouts, log),
		httpserver.New("public-http", cfg.HTTP.Addr, handler, timeouts, log),
	)
	return mgr, nil
}

// newSigner builds the access-token signer for the configured algorithm.
func newSigner(cfg config.Auth) (*jwt.Signer, error) {
	switch cfg.JWTAlgorithm {
	case "EdDSA":
		seed, err := base64.StdEncoding.DecodeString(cfg.JWTPrivateKey.Value())
		if err != nil {
			return nil, fmt.Errorf("decode AUTH_JWT_PRIVATE_KEY: %w", err)
		}
		return jwt.NewEdDSA(ed25519.NewKeyFromSeed(seed), cfg.JWTIssuer, cfg.JWTAudience)
	default:
		return jwt.NewHS256([]byte(cfg.JWTSecret.Value()), cfg.JWTIssuer, cfg.JWTAudience)
	}
}
