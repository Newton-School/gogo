package admin

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/cybersaksham/gogo/orm"
)

const adminLogTable = "gogo_admin_log"

// SQLLogStore persists Django-style admin history entries in SQL.
type SQLLogStore struct {
	Database *orm.Database
	Now      func() time.Time
}

// NewSQLLogStore creates a SQL-backed admin log store.
func NewSQLLogStore(database *orm.Database) *SQLLogStore {
	return &SQLLogStore{Database: database}
}

// EnsureSchema creates the framework admin log table when it does not exist.
func (s *SQLLogStore) EnsureSchema(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ` + s.q(adminLogTable) + ` (
			` + s.q("id") + ` BIGINT PRIMARY KEY,
			` + s.q("action_time") + ` TIMESTAMP NOT NULL,
			` + s.q("user_id") + ` BIGINT NOT NULL,
			` + s.q("content_type") + ` VARCHAR(255) NOT NULL,
			` + s.q("object_id") + ` VARCHAR(255) NOT NULL,
			` + s.q("object_repr") + ` TEXT NOT NULL,
			` + s.q("action_flag") + ` VARCHAR(32) NOT NULL,
			` + s.q("change_message") + ` TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS ` + s.q("gogo_admin_log_object_idx") + ` ON ` + s.q(adminLogTable) + ` (` + s.q("content_type") + `, ` + s.q("object_id") + `)`,
		`CREATE INDEX IF NOT EXISTS ` + s.q("gogo_admin_log_action_time_idx") + ` ON ` + s.q(adminLogTable) + ` (` + s.q("action_time") + `)`,
	}
	for _, statement := range statements {
		if _, err := s.Database.SQLDB().ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// Log appends one admin history entry.
func (s *SQLLogStore) Log(entry AdminLogEntry) error {
	ctx := context.Background()
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	if entry.ActionTime.IsZero() {
		entry.ActionTime = time.Now().UTC()
		if s.Now != nil {
			entry.ActionTime = s.Now().UTC()
		}
	}
	tx, err := s.Database.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	id, err := s.nextID(ctx, tx)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO `+s.q(adminLogTable)+` (`+s.q("id")+`, `+s.q("action_time")+`, `+s.q("user_id")+`, `+s.q("content_type")+`, `+s.q("object_id")+`, `+s.q("object_repr")+`, `+s.q("action_flag")+`, `+s.q("change_message")+`) VALUES (`+
			strings.Join([]string{s.placeholder(1), s.placeholder(2), s.placeholder(3), s.placeholder(4), s.placeholder(5), s.placeholder(6), s.placeholder(7), s.placeholder(8)}, ", ")+`)`,
		id,
		entry.ActionTime,
		entry.UserID,
		entry.ContentType,
		entry.ObjectID,
		entry.ObjectRepr,
		string(entry.ActionFlag),
		entry.ChangeMessage,
	)
	if err != nil {
		return err
	}
	err = tx.Commit()
	return err
}

// EntriesForObject returns admin history entries for one object in insertion order.
func (s *SQLLogStore) EntriesForObject(contentType, objectID string) ([]AdminLogEntry, error) {
	ctx := context.Background()
	if err := s.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.Database.SQLDB().QueryContext(ctx,
		`SELECT `+s.q("action_time")+`, `+s.q("user_id")+`, `+s.q("content_type")+`, `+s.q("object_id")+`, `+s.q("object_repr")+`, `+s.q("action_flag")+`, `+s.q("change_message")+`
		FROM `+s.q(adminLogTable)+`
		WHERE `+s.q("content_type")+` = `+s.placeholder(1)+` AND `+s.q("object_id")+` = `+s.placeholder(2)+`
		ORDER BY `+s.q("id")+` ASC`,
		contentType,
		objectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AdminLogEntry
	for rows.Next() {
		var entry AdminLogEntry
		var actionFlag string
		if err := rows.Scan(&entry.ActionTime, &entry.UserID, &entry.ContentType, &entry.ObjectID, &entry.ObjectRepr, &actionFlag, &entry.ChangeMessage); err != nil {
			return nil, err
		}
		entry.ActionFlag = ActionFlag(actionFlag)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *SQLLogStore) nextID(ctx context.Context, tx *sql.Tx) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(`+s.q("id")+`), 0) + 1 FROM `+s.q(adminLogTable)).Scan(&id)
	return id, err
}

func (s *SQLLogStore) ready() error {
	if s == nil || s.Database == nil || s.Database.SQLDB() == nil {
		return orm.ErrDatabaseNotFound
	}
	return nil
}

func (s *SQLLogStore) q(identifier string) string {
	if s.Database != nil && s.Database.Dialect != nil {
		return s.Database.Dialect.QuoteIdent(identifier)
	}
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func (s *SQLLogStore) placeholder(position int) string {
	if s.Database != nil && s.Database.Dialect != nil {
		return s.Database.Dialect.Placeholder(position)
	}
	return "?"
}

var _ AdminLogStore = (*SQLLogStore)(nil)
