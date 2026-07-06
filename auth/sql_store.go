package auth

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/orm"
)

// SQLUserStore persists built-in auth users in the auth_user table.
type SQLUserStore struct {
	database *orm.Database
}

// NewSQLUserStore creates a database-backed auth user store.
func NewSQLUserStore(database *orm.Database) *SQLUserStore {
	return &SQLUserStore{database: database}
}

// Add inserts one auth user.
func (s *SQLUserStore) Add(user User) error {
	if err := s.ready(); err != nil {
		return err
	}
	ctx := context.Background()
	user = normalizeSQLUser(user)
	if user.ID == 0 {
		nextID, err := s.nextID(ctx)
		if err != nil {
			return err
		}
		user.ID = nextID
	}
	_, err := s.database.SQLDB().ExecContext(ctx,
		"INSERT INTO "+s.q("auth_user")+" ("+s.columnList(sqlUserInsertColumns())+") VALUES ("+s.placeholderList(len(sqlUserInsertColumns()))+")",
		sqlUserValues(user)...,
	)
	return err
}

// FindByUsername returns a user by normalized username.
func (s *SQLUserStore) FindByUsername(ctx context.Context, username string) (User, bool, error) {
	return s.findOne(ctx, "username", NormalizeUsername(username))
}

// FindByEmail returns a user by normalized email.
func (s *SQLUserStore) FindByEmail(ctx context.Context, email string) (User, bool, error) {
	return s.findOne(ctx, "email", NormalizeEmail(email))
}

// FindByID returns a user by primary key.
func (s *SQLUserStore) FindByID(ctx context.Context, id int64) (User, bool, error) {
	return s.findOne(ctx, "id", id)
}

// UpdateUser replaces an existing auth user.
func (s *SQLUserStore) UpdateUser(ctx context.Context, user User) error {
	if err := s.ready(); err != nil {
		return err
	}
	if user.ID == 0 {
		return ErrUserNotFound
	}
	user = normalizeSQLUser(user)
	assignments := make([]string, 0, 10)
	columns := sqlUserUpdateColumns()
	for index, column := range columns {
		assignments = append(assignments, s.q(column)+" = "+s.p(index+1))
	}
	args := sqlUserUpdateValues(user)
	args = append(args, user.ID)
	statement := "UPDATE " + s.q("auth_user") + " SET " + strings.Join(assignments, ", ") + " WHERE " + s.q("id") + " = " + s.p(len(args))
	result, err := s.database.SQLDB().ExecContext(ctx, statement, args...)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// UpdateLastLogin stores the latest successful login timestamp.
func (s *SQLUserStore) UpdateLastLogin(ctx context.Context, userID int64, at time.Time) error {
	if err := s.ready(); err != nil {
		return err
	}
	result, err := s.database.SQLDB().ExecContext(ctx, "UPDATE "+s.q("auth_user")+" SET "+s.q("last_login")+" = "+s.p(1)+" WHERE "+s.q("id")+" = "+s.p(2), at.UTC(), userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *SQLUserStore) findOne(ctx context.Context, column string, value any) (User, bool, error) {
	if err := s.ready(); err != nil {
		return User{}, false, err
	}
	query := "SELECT " + s.columnList(sqlUserSelectColumns()) + " FROM " + s.q("auth_user") + " WHERE " + s.q(column) + " = " + s.p(1)
	user, err := scanSQLUser(s.database.SQLDB().QueryRowContext(ctx, query, value))
	if err == sql.ErrNoRows {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	return user, true, nil
}

func (s *SQLUserStore) nextID(ctx context.Context) (int64, error) {
	var next int64
	if err := s.database.SQLDB().QueryRowContext(ctx, "SELECT COALESCE(MAX("+s.q("id")+"), 0) + 1 FROM "+s.q("auth_user")).Scan(&next); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *SQLUserStore) ready() error {
	if s == nil || s.database == nil || s.database.SQLDB() == nil {
		return ErrUserStoreRequired
	}
	return nil
}

func (s *SQLUserStore) q(identifier string) string {
	if s.database != nil && s.database.Dialect != nil {
		return s.database.Dialect.QuoteIdent(identifier)
	}
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func (s *SQLUserStore) p(position int) string {
	if s.database != nil && s.database.Dialect != nil {
		return s.database.Dialect.Placeholder(position)
	}
	return "?"
}

func (s *SQLUserStore) columnList(columns []string) string {
	quoted := make([]string, len(columns))
	for index, column := range columns {
		quoted[index] = s.q(column)
	}
	return strings.Join(quoted, ", ")
}

func (s *SQLUserStore) placeholderList(count int) string {
	placeholders := make([]string, count)
	for index := range placeholders {
		placeholders[index] = s.p(index + 1)
	}
	return strings.Join(placeholders, ", ")
}

func sqlUserSelectColumns() []string {
	return []string{"id", "password", "last_login", "is_superuser", "username", "first_name", "last_name", "email", "is_staff", "is_active", "date_joined"}
}

func sqlUserInsertColumns() []string {
	return sqlUserSelectColumns()
}

func sqlUserUpdateColumns() []string {
	return []string{"password", "last_login", "is_superuser", "username", "first_name", "last_name", "email", "is_staff", "is_active", "date_joined"}
}

func sqlUserValues(user User) []any {
	return []any{
		user.ID,
		user.Password,
		sqlTimeOrNil(user.LastLogin),
		user.IsSuperuser,
		user.Username,
		user.FirstName,
		user.LastName,
		user.Email,
		user.IsStaff,
		user.IsActive,
		user.DateJoined.UTC(),
	}
}

func sqlUserUpdateValues(user User) []any {
	values := sqlUserValues(user)
	return values[1:]
}

func normalizeSQLUser(user User) User {
	user.Username = NormalizeUsername(user.Username)
	user.Email = NormalizeEmail(user.Email)
	if user.DateJoined.IsZero() {
		user.DateJoined = time.Now().UTC()
	}
	return user
}

func sqlTimeOrNil(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func scanSQLUser(scanner interface{ Scan(...any) error }) (User, error) {
	var user User
	var lastLogin any
	var isSuperuser any
	var isStaff any
	var isActive any
	var dateJoined any
	if err := scanner.Scan(
		&user.ID,
		&user.Password,
		&lastLogin,
		&isSuperuser,
		&user.Username,
		&user.FirstName,
		&user.LastName,
		&user.Email,
		&isStaff,
		&isActive,
		&dateJoined,
	); err != nil {
		return User{}, err
	}
	user.LastLogin = sqlTimeValue(lastLogin)
	user.IsSuperuser = sqlBoolValue(isSuperuser)
	user.IsStaff = sqlBoolValue(isStaff)
	user.IsActive = sqlBoolValue(isActive)
	user.DateJoined = sqlTimeValue(dateJoined)
	return user, nil
}

func sqlBoolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case int64:
		return typed != 0
	case int:
		return typed != 0
	case []byte:
		return sqlBoolString(string(typed))
	case string:
		return sqlBoolString(typed)
	default:
		return false
	}
}

func sqlBoolString(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if parsed, err := strconv.ParseBool(normalized); err == nil {
		return parsed
	}
	integer, _ := strconv.ParseInt(normalized, 10, 64)
	return integer != 0
}

func sqlTimeValue(value any) time.Time {
	switch typed := value.(type) {
	case nil:
		return time.Time{}
	case time.Time:
		return typed.UTC()
	case []byte:
		return parseSQLTime(string(typed))
	case string:
		return parseSQLTime(typed)
	default:
		return time.Time{}
	}
}

func parseSQLTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", value, time.UTC); err == nil {
		return parsed.UTC()
	}
	return time.Time{}
}

func (s *SQLUserStore) String() string {
	if s == nil || s.database == nil {
		return "sql auth user store"
	}
	return fmt.Sprintf("sql auth user store %s", s.database.Name)
}
