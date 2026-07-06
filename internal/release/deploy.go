package release

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Newton-School/gogo/checks"
	"github.com/Newton-School/gogo/conf"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const deployCheckTimeout = 2 * time.Second

// DeployConfig contains the runtime state required for production readiness checks.
type DeployConfig struct {
	Settings conf.Settings

	DatabaseReachable      bool
	DatabaseError          error
	StaticFilesCollected   bool
	MediaStorageWritable   bool
	MediaStorageError      error
	QueueBrokerReachable   bool
	QueueBrokerError       error
	ResultBackendReachable bool
	ResultBackendError     error
	ScheduleStoreReachable bool
	ScheduleStoreError     error
}

// BuildDeployConfig runs reachability and filesystem checks for deploy validation.
func BuildDeployConfig(ctx context.Context, settings conf.Settings) DeployConfig {
	config := DeployConfig{Settings: settings}

	config.DatabaseError = CheckDatabaseReachable(ctx, settings.DatabaseURL)
	config.DatabaseReachable = config.DatabaseError == nil
	config.StaticFilesCollected = settings.StaticFilesCollected || directoryHasFiles(settings.StaticRoot)
	config.MediaStorageError = CheckMediaStorageWritable(settings.MediaRoot)
	config.MediaStorageWritable = config.MediaStorageError == nil

	if strings.TrimSpace(settings.BrokerURL) != "" {
		config.QueueBrokerError = CheckEndpointReachable(ctx, settings.BrokerURL)
		config.QueueBrokerReachable = config.QueueBrokerError == nil
	}
	if strings.TrimSpace(settings.ResultBackend) != "" {
		config.ResultBackendError = CheckEndpointReachable(ctx, settings.ResultBackend)
		config.ResultBackendReachable = config.ResultBackendError == nil
	}
	if strings.TrimSpace(settings.ScheduleStore) != "" {
		config.ScheduleStoreError = CheckEndpointReachable(ctx, settings.ScheduleStore)
		config.ScheduleStoreReachable = config.ScheduleStoreError == nil
	}

	return config
}

// RunDeployChecks validates production readiness and returns only failing checks.
func RunDeployChecks(config DeployConfig) []checks.Result {
	settings := config.Settings
	var results []checks.Result

	if settings.Debug {
		results = append(results, deployResult("deploy.E001", "debug must be disabled", "Set GOGO_DEBUG=false for production.", "GOGO_DEBUG"))
	}
	if !strongSecret(settings.SecretKey) {
		results = append(results, deployResult("deploy.E002", "secret key is not strong enough", "Use at least 32 random characters from a secret manager.", "GOGO_SECRET_KEY"))
	}
	if !explicitAllowedHosts(settings.AllowedHosts) {
		results = append(results, deployResult("deploy.E003", "allowed hosts must be explicit", "Set GOGO_ALLOWED_HOSTS to exact production hostnames and never '*'.", "GOGO_ALLOWED_HOSTS"))
	}
	if !settings.SessionCookieSecure {
		results = append(results, deployResult("deploy.E004", "session cookies must be secure", "Set GOGO_SESSION_COOKIE_SECURE=true.", "GOGO_SESSION_COOKIE_SECURE"))
	}
	if !settings.CSRFCookieSecure {
		results = append(results, deployResult("deploy.E005", "CSRF cookies must be secure", "Set GOGO_CSRF_COOKIE_SECURE=true.", "GOGO_CSRF_COOKIE_SECURE"))
	}
	if !settings.HTTPSEnabled {
		results = append(results, deployResult("deploy.E006", "HTTPS must be enabled", "Set GOGO_HTTPS_ENABLED=true after TLS redirects and secure proxy handling are configured.", "GOGO_HTTPS_ENABLED"))
	}
	if err := validateCSRFTrustedOrigins(settings.CSRFTrustedOrigins); err != nil {
		results = append(results, deployResult("deploy.E007", "CSRF trusted origins are invalid", err.Error(), "GOGO_CSRF_TRUSTED_ORIGINS"))
	}
	if !config.DatabaseReachable {
		results = append(results, deployResult("deploy.E008", "database is not reachable", errorHint(config.DatabaseError, "Check DATABASE_URL and database network access."), "DATABASE_URL"))
	}
	if !settings.MigrationsApplied {
		results = append(results, deployResult("deploy.E009", "migrations are not confirmed applied", "Run gogo migrate, verify gogo showmigrations, then set GOGO_DEPLOY_MIGRATIONS_APPLIED=true for the release job.", "GOGO_DEPLOY_MIGRATIONS_APPLIED"))
	}
	if !config.StaticFilesCollected {
		results = append(results, deployResult("deploy.E010", "static files are not confirmed collected", "Run collectstatic or set GOGO_DEPLOY_STATIC_COLLECTED=true after the static artifact is present.", "GOGO_STATIC_ROOT"))
	}
	if !config.MediaStorageWritable {
		results = append(results, deployResult("deploy.E011", "media storage is not writable", errorHint(config.MediaStorageError, "Set GOGO_MEDIA_ROOT to a writable directory."), "GOGO_MEDIA_ROOT"))
	}
	if !validAdminPath(settings.AdminPath) {
		results = append(results, deployResult("deploy.E012", "admin path is invalid", "Set GOGO_ADMIN_PATH to an absolute path such as /admin.", "GOGO_ADMIN_PATH"))
	}
	if !settings.AdminPathReviewed {
		results = append(results, deployResult("deploy.E013", "admin path has not been reviewed", "Review admin exposure, then set GOGO_ADMIN_PATH_REVIEWED=true.", "GOGO_ADMIN_PATH_REVIEWED"))
	}
	if strings.TrimSpace(settings.BrokerURL) != "" && !config.QueueBrokerReachable {
		results = append(results, deployResult("deploy.E014", "queue broker is not reachable", errorHint(config.QueueBrokerError, "Check GOGO_BROKER_URL and broker network access."), "GOGO_BROKER_URL"))
	}
	if strings.TrimSpace(settings.ResultBackend) != "" && !config.ResultBackendReachable {
		results = append(results, deployResult("deploy.E015", "result backend is not reachable", errorHint(config.ResultBackendError, "Check GOGO_RESULT_BACKEND and result backend network access."), "GOGO_RESULT_BACKEND"))
	}
	if strings.TrimSpace(settings.ScheduleStore) != "" && !config.ScheduleStoreReachable {
		results = append(results, deployResult("deploy.E019", "schedule store is not reachable", errorHint(config.ScheduleStoreError, "Check GOGO_SCHEDULE_STORE and schedule store network access."), "GOGO_SCHEDULE_STORE"))
	}
	if settings.PasswordResetEnabled && strings.TrimSpace(settings.EmailURL) == "" {
		results = append(results, deployResult("deploy.E016", "email backend is required for password reset", "Set GOGO_EMAIL_URL or disable password reset.", "GOGO_EMAIL_URL"))
	}
	if isMemoryEndpoint(settings.BrokerURL) {
		results = append(results, deployResult("deploy.E017", "memory queue broker is not allowed in production", "Use a durable broker such as Redis for production workers, or leave GOGO_BROKER_URL empty when queues are not enabled.", "GOGO_BROKER_URL"))
	}
	if isMemoryEndpoint(settings.ResultBackend) {
		results = append(results, deployResult("deploy.E018", "memory result backend is not allowed in production", "Use a durable result backend such as Redis or SQL for production workers, or leave GOGO_RESULT_BACKEND empty when queues are not enabled.", "GOGO_RESULT_BACKEND"))
	}
	if isMemoryEndpoint(settings.ScheduleStore) {
		results = append(results, deployResult("deploy.E020", "memory schedule store is not allowed in production", "Use a durable schedule store such as Redis for production beat, or leave GOGO_SCHEDULE_STORE empty when beat is not enabled.", "GOGO_SCHEDULE_STORE"))
	}

	if len(results) == 0 {
		return []checks.Result{{ID: "deploy.I001", Tags: []string{"deploy"}, Severity: checks.SeverityInfo, Message: "production deploy checks passed"}}
	}
	return results
}

// CheckDatabaseReachable verifies DATABASE_URL with a short timeout.
func CheckDatabaseReachable(ctx context.Context, databaseURL string) error {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		return errors.New("DATABASE_URL is empty")
	}
	driver, dsn, err := databaseDriverAndDSN(databaseURL)
	if err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, deployCheckTimeout)
	defer cancel()
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.PingContext(checkCtx)
}

// CheckEndpointReachable verifies broker-like URLs with a short TCP probe.
func CheckEndpointReachable(ctx context.Context, rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("endpoint URL is empty")
	}
	if rawURL == "memory" || strings.HasPrefix(rawURL, "memory://") || strings.HasPrefix(rawURL, "sql://") {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	host := parsed.Host
	switch parsed.Scheme {
	case "redis", "rediss":
		host = hostWithDefaultPort(host, "6379")
	case "amqp", "amqps":
		host = hostWithDefaultPort(host, "5672")
	default:
		return fmt.Errorf("unsupported endpoint scheme %q", parsed.Scheme)
	}
	dialer := net.Dialer{Timeout: deployCheckTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return err
	}
	return conn.Close()
}

func isMemoryEndpoint(rawURL string) bool {
	value := strings.ToLower(strings.TrimSpace(rawURL))
	return value == "memory" || strings.HasPrefix(value, "memory://")
}

// CheckMediaStorageWritable verifies that media storage exists and accepts writes.
func CheckMediaStorageWritable(mediaRoot string) error {
	mediaRoot = strings.TrimSpace(mediaRoot)
	if mediaRoot == "" {
		return errors.New("GOGO_MEDIA_ROOT is empty")
	}
	info, err := os.Stat(mediaRoot)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", mediaRoot)
	}
	file, err := os.CreateTemp(mediaRoot, ".gogo-write-check-*")
	if err != nil {
		return err
	}
	name := file.Name()
	if _, err := file.WriteString("ok"); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func databaseDriverAndDSN(databaseURL string) (string, string, error) {
	switch {
	case strings.HasPrefix(databaseURL, "sqlite://"):
		dsn := strings.TrimPrefix(databaseURL, "sqlite://")
		if dsn == "" {
			return "", "", errors.New("sqlite database path is empty")
		}
		return "sqlite", dsn, nil
	case strings.HasPrefix(databaseURL, "postgres://"), strings.HasPrefix(databaseURL, "postgresql://"):
		return "pgx", databaseURL, nil
	default:
		return "", "", fmt.Errorf("unsupported database URL scheme")
	}
}

func directoryHasFiles(root string) bool {
	root = strings.TrimSpace(root)
	if root == "" {
		return false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

func strongSecret(secret string) bool {
	secret = strings.TrimSpace(secret)
	if len(secret) < 32 {
		return false
	}
	lower := strings.ToLower(secret)
	for _, unsafe := range []string{"secret", "password", "changeme", "development", "dev-", "test-"} {
		if strings.Contains(lower, unsafe) {
			return false
		}
	}
	first := secret[0]
	for i := 1; i < len(secret); i++ {
		if secret[i] != first {
			return true
		}
	}
	return false
}

func explicitAllowedHosts(hosts []string) bool {
	if len(hosts) == 0 {
		return false
	}
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" || host == "*" || strings.Contains(host, "://") {
			return false
		}
	}
	return true
}

func validateCSRFTrustedOrigins(origins []string) error {
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("%s must be an https origin", origin)
		}
		if parsed.Path != "" && parsed.Path != "/" {
			return fmt.Errorf("%s must not include a path", origin)
		}
		if strings.Contains(parsed.Host, "*") {
			return fmt.Errorf("%s must not contain wildcards", origin)
		}
	}
	return nil
}

func validAdminPath(path string) bool {
	path = strings.TrimSpace(path)
	return strings.HasPrefix(path, "/") && !strings.Contains(path, "://") && !strings.Contains(path, " ") && path != "/"
}

func hostWithDefaultPort(host string, fallback string) string {
	if host == "" {
		return net.JoinHostPort("localhost", fallback)
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, fallback)
}

func deployResult(id string, message string, hint string, object string) checks.Result {
	return checks.Result{ID: id, Tags: []string{"deploy"}, Severity: checks.SeverityError, Message: message, Hint: hint, Object: object}
}

func errorHint(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	return fallback + " Detail: " + err.Error()
}
