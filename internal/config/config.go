// Package config loads the process configuration from environment
// variables into typed structs and validates it at startup (ADR-0016).
//
// Rules:
//   - Only cmd/* imports this package. Modules receive their own small
//     config structs from the composition root, never Config.
//   - Every variable is documented in .env.example.
//   - Secrets use the Secret type, which never prints its value.
//   - Startup fails with ALL problems listed, not just the first.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is the complete process configuration.
type Config struct {
	App       App       `envPrefix:"APP_"`
	Log       Log       `envPrefix:"LOG_"`
	HTTP      HTTP      `envPrefix:"HTTP_"`
	Admin     Admin     `envPrefix:"ADMIN_"`
	Shutdown  Shutdown  `envPrefix:"SHUTDOWN_"`
	Postgres  Postgres  `envPrefix:"POSTGRES_"`
	Redis     Redis     `envPrefix:"REDIS_"`
	Cache     Cache     `envPrefix:"CACHE_"`
	RateLimit RateLimit `envPrefix:"RATELIMIT_"`
	Auth      Auth      `envPrefix:"AUTH_"`
	Jobs      Jobs      `envPrefix:"JOBS_"`
	Note      Note      `envPrefix:"NOTE_"`
}

// Environment names.
const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

// App identifies the service and its environment.
type App struct {
	Name string `env:"NAME" envDefault:"goplate"`
	// Env only changes safety defaults (cookie Secure flag, log format,
	// docs); it never switches code paths.
	Env string `env:"ENV" envDefault:"production"`
}

// Log configures structured logging.
type Log struct {
	Level  slog.Level `env:"LEVEL" envDefault:"info"`
	Format string     `env:"FORMAT" envDefault:"json"` // json | text
}

// HTTP configures the public API server.
type HTTP struct {
	Addr              string         `env:"ADDR" envDefault:":8080"`
	ReadHeaderTimeout time.Duration  `env:"READ_HEADER_TIMEOUT" envDefault:"5s"`
	ReadTimeout       time.Duration  `env:"READ_TIMEOUT" envDefault:"15s"`
	WriteTimeout      time.Duration  `env:"WRITE_TIMEOUT" envDefault:"30s"`
	IdleTimeout       time.Duration  `env:"IDLE_TIMEOUT" envDefault:"120s"`
	MaxBodyBytes      int64          `env:"MAX_BODY_BYTES" envDefault:"1048576"`
	TrustedOrigins    []string       `env:"TRUSTED_ORIGINS" envSeparator:","` // CORS + CSRF allow-list
	TrustedProxies    []netip.Prefix `env:"TRUSTED_PROXIES" envSeparator:","` // CIDRs allowed to set X-Forwarded-For
	DocsEnabled       bool           `env:"DOCS_ENABLED" envDefault:"false"`
}

// Admin configures the internal operations server (metrics, readiness details, pprof).
type Admin struct {
	Addr  string `env:"ADDR" envDefault:":9090"` // never expose publicly
	Pprof bool   `env:"PPROF" envDefault:"false"`
}

// Shutdown bounds graceful shutdown (ADR-0012).
type Shutdown struct {
	DrainDelay time.Duration `env:"DRAIN_DELAY" envDefault:"5s"`
	Timeout    time.Duration `env:"TIMEOUT" envDefault:"20s"` // keep below the orchestrator's grace period
}

// Postgres configures the connection pool.
type Postgres struct {
	URL             Secret        `env:"URL,required,notEmpty"`
	MaxConns        int32         `env:"MAX_CONNS" envDefault:"10"`
	MinConns        int32         `env:"MIN_CONNS" envDefault:"2"`
	MaxConnLifetime time.Duration `env:"MAX_CONN_LIFETIME" envDefault:"1h"`
	MaxConnIdleTime time.Duration `env:"MAX_CONN_IDLE_TIME" envDefault:"30m"`
	MigrateOnStart  bool          `env:"MIGRATE_ON_START" envDefault:"false"` // single-instance dev only
}

// Redis configures the shared Redis connection.
type Redis struct {
	URL Secret `env:"URL"` // required when any feature uses the redis driver
}

// Cache selects the cache adapter (ADR-0009).
type Cache struct {
	Driver string `env:"DRIVER" envDefault:"redis"` // redis | memory
}

// RateLimit configures rate limiting policies (ADR-0013).
type RateLimit struct {
	Driver          string        `env:"DRIVER" envDefault:"redis"` // redis | memory
	Rate            int           `env:"RATE" envDefault:"100"`
	Period          time.Duration `env:"PERIOD" envDefault:"1m"`
	Burst           int           `env:"BURST" envDefault:"20"`
	LoginIPRate     int           `env:"LOGIN_IP_RATE" envDefault:"5"`     // per minute
	LoginEmailRate  int           `env:"LOGIN_EMAIL_RATE" envDefault:"10"` // per hour
	LoginFailClosed bool          `env:"LOGIN_FAIL_CLOSED" envDefault:"true"`
}

// Auth configures tokens and their transport (ADR-0007, ADR-0008).
type Auth struct {
	JWTAlgorithm    string        `env:"JWT_ALGORITHM" envDefault:"HS256"` // HS256 | EdDSA
	JWTSecret       Secret        `env:"JWT_SECRET"`                       // HS256: >= 32 bytes
	JWTPrivateKey   Secret        `env:"JWT_PRIVATE_KEY"`                  // EdDSA: base64 Ed25519 seed (32 bytes)
	JWTIssuer       string        `env:"JWT_ISSUER" envDefault:"goplate"`
	JWTAudience     string        `env:"JWT_AUDIENCE" envDefault:"goplate-api"`
	AccessTTL       time.Duration `env:"ACCESS_TTL" envDefault:"15m"`
	RefreshTTL      time.Duration `env:"REFRESH_TTL" envDefault:"720h"`
	BearerEnabled   bool          `env:"BEARER_ENABLED" envDefault:"true"`
	CookieEnabled   bool          `env:"COOKIE_ENABLED" envDefault:"true"`
	CookieDomain    string        `env:"COOKIE_DOMAIN"`
	DenylistEnabled bool          `env:"DENYLIST_ENABLED" envDefault:"false"`
}

// Jobs configures background job processing (ADR-0010).
type Jobs struct {
	WorkerEnabled bool `env:"WORKER_ENABLED" envDefault:"true"`
	MaxWorkers    int  `env:"MAX_WORKERS" envDefault:"10"`
}

// Note configures the note module.
type Note struct {
	CacheTTL time.Duration `env:"CACHE_TTL" envDefault:"5m"`
}

// Load parses environ (KEY -> value; use env.ToMap(os.Environ())) and
// validates the result. Taking a map instead of reading the process
// environment keeps tests hermetic.
func Load(environ map[string]string) (*Config, error) {
	var cfg Config
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: environ}); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &cfg, nil
}

// Validate checks cross-field rules that struct tags cannot express.
func (c *Config) Validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}

	check(slices.Contains([]string{EnvDevelopment, EnvTest, EnvProduction}, c.App.Env),
		"APP_ENV must be development, test or production")
	check(c.Log.Format == "json" || c.Log.Format == "text", "LOG_FORMAT must be json or text")
	check(c.Cache.Driver == "redis" || c.Cache.Driver == "memory", "CACHE_DRIVER must be redis or memory")
	check(c.RateLimit.Driver == "redis" || c.RateLimit.Driver == "memory", "RATELIMIT_DRIVER must be redis or memory")
	check(c.RateLimit.Rate > 0 && c.RateLimit.Period > 0, "RATELIMIT_RATE and RATELIMIT_PERIOD must be positive")
	check(c.Shutdown.Timeout > 0, "SHUTDOWN_TIMEOUT must be positive")
	check(c.Postgres.MinConns <= c.Postgres.MaxConns, "POSTGRES_MIN_CONNS must not exceed POSTGRES_MAX_CONNS")
	check(!c.UsesRedis() || c.Redis.URL != "",
		"REDIS_URL is required when CACHE_DRIVER/RATELIMIT_DRIVER is redis or AUTH_DENYLIST_ENABLED is true")

	switch c.Auth.JWTAlgorithm {
	case "HS256":
		check(len(c.Auth.JWTSecret) >= 32, "AUTH_JWT_SECRET must be at least 32 bytes for HS256")
	case "EdDSA":
		seed, err := base64.StdEncoding.DecodeString(c.Auth.JWTPrivateKey.Value())
		check(err == nil && len(seed) == 32, "AUTH_JWT_PRIVATE_KEY must be a base64-encoded 32-byte Ed25519 seed")
	default:
		errs = append(errs, errors.New("AUTH_JWT_ALGORITHM must be HS256 or EdDSA"))
	}
	check(c.Auth.AccessTTL > 0 && c.Auth.AccessTTL <= time.Hour, "AUTH_ACCESS_TTL must be between 0 and 1h (keep access tokens short-lived)")
	check(c.Auth.RefreshTTL > c.Auth.AccessTTL, "AUTH_REFRESH_TTL must be longer than AUTH_ACCESS_TTL")
	check(c.Auth.BearerEnabled || c.Auth.CookieEnabled, "enable at least one of AUTH_BEARER_ENABLED, AUTH_COOKIE_ENABLED")
	check(!c.Auth.CookieEnabled || !slices.Contains(c.HTTP.TrustedOrigins, "*"),
		"HTTP_TRUSTED_ORIGINS cannot contain * when cookies are enabled (credentials would be exposed to any site)")

	return errors.Join(errs...)
}

// UsesRedis reports whether any feature needs a Redis connection.
func (c *Config) UsesRedis() bool {
	return c.Cache.Driver == "redis" || c.RateLimit.Driver == "redis" || c.Auth.DenylistEnabled
}

// IsDevelopment reports whether APP_ENV=development.
func (c *Config) IsDevelopment() bool { return c.App.Env == EnvDevelopment }

// Secret is a string that never reveals itself in logs, JSON or fmt output.
// Call Value to use it.
type Secret string

const redacted = "REDACTED"

// Value returns the real secret.
func (s Secret) Value() string { return string(s) }

func (s Secret) String() string               { return redacted }
func (s Secret) GoString() string             { return redacted }
func (s Secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
