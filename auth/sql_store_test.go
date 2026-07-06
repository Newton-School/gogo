package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Newton-School/gogo/orm"
)

func TestSQLUserStorePersistsAndLoadsUsers(t *testing.T) {
	ctx := context.Background()
	database, err := orm.OpenDatabaseURL(ctx, orm.DefaultDatabase, "sqlite://"+filepath.Join(t.TempDir(), "db.sqlite3"))
	if err != nil {
		t.Fatalf("OpenDatabaseURL() error = %v", err)
	}
	defer database.Close()
	if _, err := database.SQLDB().ExecContext(ctx, `CREATE TABLE auth_user (
	id BIGINT PRIMARY KEY,
	password VARCHAR(128) NOT NULL,
	last_login TIMESTAMP NULL,
	is_superuser BOOLEAN NOT NULL DEFAULT FALSE,
	username VARCHAR(150) NOT NULL UNIQUE,
	first_name VARCHAR(150) NOT NULL DEFAULT '',
	last_name VARCHAR(150) NOT NULL DEFAULT '',
	email VARCHAR(254) NOT NULL DEFAULT '',
	is_staff BOOLEAN NOT NULL DEFAULT FALSE,
	is_active BOOLEAN NOT NULL DEFAULT TRUE,
	date_joined TIMESTAMP NOT NULL
)`); err != nil {
		t.Fatalf("create auth_user: %v", err)
	}

	store := NewSQLUserStore(database)
	joined := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	if err := store.Add(User{AbstractUser: AbstractUser{
		AbstractBaseUser: AbstractBaseUser{
			Password:      "hash",
			IsSuperuser:   true,
			IsActive:      true,
			DateJoined:    joined,
			Authenticated: true,
		},
		Username: "Admin",
		Email:    "ADMIN@example.com",
		IsStaff:  true,
	}}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	user, ok, err := store.FindByUsername(ctx, "admin")
	if err != nil || !ok {
		t.Fatalf("FindByUsername() = %#v, %v, %v", user, ok, err)
	}
	if user.ID != 1 || user.Username != "admin" || user.Email != "admin@example.com" || !user.IsStaff || !user.IsSuperuser || !user.IsActive {
		t.Fatalf("stored user = %#v", user)
	}

	login := time.Date(2026, 6, 30, 11, 0, 0, 0, time.UTC)
	if err := store.UpdateLastLogin(ctx, user.ID, login); err != nil {
		t.Fatalf("UpdateLastLogin() error = %v", err)
	}
	loaded, ok, err := store.FindByID(ctx, user.ID)
	if err != nil || !ok {
		t.Fatalf("FindByID() = %#v, %v, %v", loaded, ok, err)
	}
	if !loaded.LastLogin.Equal(login) {
		t.Fatalf("last login = %s, want %s", loaded.LastLogin, login)
	}
}
