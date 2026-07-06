package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Newton-School/gogo/auth"
)

type fileAuthUserStore struct {
	mu   sync.Mutex
	path func() string
}

func newFileAuthUserStore(path func() string) *fileAuthUserStore {
	return &fileAuthUserStore{path: path}
}

func defaultCLIAuthStorePath() string {
	root := discoverProjectRoot()
	return filepath.Join(root, ".gogo", "auth_users.json")
}

func discoverProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

func (s *fileAuthUserStore) Add(user auth.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.load()
	if err != nil {
		return err
	}
	if err := store.Add(user); err != nil {
		return err
	}
	return s.save(store.Users())
}

func (s *fileAuthUserStore) FindByUsername(ctx context.Context, username string) (auth.User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.load()
	if err != nil {
		return auth.User{}, false, err
	}
	return store.FindByUsername(ctx, username)
}

func (s *fileAuthUserStore) FindByEmail(ctx context.Context, email string) (auth.User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.load()
	if err != nil {
		return auth.User{}, false, err
	}
	return store.FindByEmail(ctx, email)
}

func (s *fileAuthUserStore) FindByID(ctx context.Context, id int64) (auth.User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.load()
	if err != nil {
		return auth.User{}, false, err
	}
	return store.FindByID(ctx, id)
}

func (s *fileAuthUserStore) UpdateUser(ctx context.Context, user auth.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.load()
	if err != nil {
		return err
	}
	if err := store.UpdateUser(ctx, user); err != nil {
		return err
	}
	return s.save(store.Users())
}

func (s *fileAuthUserStore) UpdateLastLogin(ctx context.Context, userID int64, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.load()
	if err != nil {
		return err
	}
	if err := store.UpdateLastLogin(ctx, userID, at); err != nil {
		return err
	}
	return s.save(store.Users())
}

func (s *fileAuthUserStore) load() (*auth.MemoryUserStore, error) {
	path := s.path()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return auth.NewMemoryUserStore()
	}
	if err != nil {
		return nil, err
	}
	var users []auth.User
	if len(data) > 0 {
		if err := json.Unmarshal(data, &users); err != nil {
			return nil, err
		}
	}
	return auth.NewMemoryUserStore(users...)
}

func (s *fileAuthUserStore) save(users []auth.User) error {
	path := s.path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
