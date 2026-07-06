package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/Newton-School/gogo/orm/dialects"
)

const DefaultDatabase = "default"

var (
	ErrInvalidDatabaseConfig = errors.New("invalid database config")
	ErrDatabaseExists        = errors.New("database already exists")
	ErrDatabaseNotFound      = errors.New("database not found")
)

// Logger is the database logger contract.
type Logger interface {
	Printf(format string, args ...any)
}

// HealthCheck verifies a SQL handle is usable.
type HealthCheck func(context.Context, *sql.DB) error

// DatabaseConfig configures one named database connection.
type DatabaseConfig struct {
	Name        string
	Driver      string
	DSN         string
	Dialect     dialects.Dialect
	Logger      Logger
	HealthCheck HealthCheck
}

// Database stores one named database connection and its ORM metadata.
type Database struct {
	Name        string
	Driver      string
	DSN         string
	Dialect     dialects.Dialect
	Logger      Logger
	HealthCheck HealthCheck

	mu     sync.RWMutex
	sqlDB  *sql.DB
	closed bool
}

// OpenDatabase opens and verifies one database connection.
func OpenDatabase(ctx context.Context, config DatabaseConfig) (*Database, error) {
	if config.Name == "" {
		config.Name = DefaultDatabase
	}
	if config.Driver == "" {
		return nil, fmt.Errorf("%w: driver is required", ErrInvalidDatabaseConfig)
	}

	handle, err := sql.Open(config.Driver, config.DSN)
	if err != nil {
		return nil, err
	}

	database := &Database{
		Name:        config.Name,
		Driver:      config.Driver,
		DSN:         config.DSN,
		Dialect:     config.Dialect,
		Logger:      config.Logger,
		HealthCheck: config.HealthCheck,
		sqlDB:       handle,
	}
	if err := database.Ping(ctx); err != nil {
		_ = handle.Close()
		return nil, err
	}
	database.log("opened database %s using driver %s", database.Name, database.Driver)
	return database, nil
}

// SQLDB returns the underlying database/sql handle.
func (d *Database) SQLDB() *sql.DB {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.sqlDB
}

// Ping verifies the database connection.
func (d *Database) Ping(ctx context.Context) error {
	handle := d.SQLDB()
	if handle == nil {
		return fmt.Errorf("%w: %s", ErrDatabaseNotFound, d.Name)
	}
	if d.HealthCheck != nil {
		return d.HealthCheck(ctx, handle)
	}
	return handle.PingContext(ctx)
}

// Stats returns current SQL connection pool stats.
func (d *Database) Stats() sql.DBStats {
	handle := d.SQLDB()
	if handle == nil {
		return sql.DBStats{}
	}
	return handle.Stats()
}

// Close closes the underlying SQL handle.
func (d *Database) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sqlDB == nil || d.closed {
		return nil
	}
	d.closed = true
	d.log("closing database %s", d.Name)
	return d.sqlDB.Close()
}

func (d *Database) log(format string, args ...any) {
	if d.Logger != nil {
		d.Logger.Printf(format, args...)
	}
}

// Connections stores named database connections.
type Connections struct {
	mu        sync.RWMutex
	databases map[string]*Database
}

// NewConnections creates a database connection registry.
func NewConnections(databases ...*Database) *Connections {
	connections := &Connections{databases: make(map[string]*Database)}
	for _, database := range databases {
		_ = connections.Add(database)
	}
	return connections
}

// Add registers one named database.
func (c *Connections) Add(database *Database) error {
	if database == nil || database.Name == "" {
		return fmt.Errorf("%w: database name is required", ErrInvalidDatabaseConfig)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.databases[database.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDatabaseExists, database.Name)
	}
	c.databases[database.Name] = database
	return nil
}

// Get returns one named database.
func (c *Connections) Get(name string) (*Database, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	database, ok := c.databases[name]
	return database, ok
}

// Default returns the default database when configured.
func (c *Connections) Default() (*Database, bool) {
	return c.Get(DefaultDatabase)
}

// Close closes all registered database handles.
func (c *Connections) Close() error {
	c.mu.RLock()
	databases := make([]*Database, 0, len(c.databases))
	for _, database := range c.databases {
		databases = append(databases, database)
	}
	c.mu.RUnlock()

	var errs []error
	for _, database := range databases {
		if err := database.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
