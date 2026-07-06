package api

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/models"
)

// Token is the framework-owned API token model.
type Token struct {
	Key       string
	UserID    int64
	User      auth.User
	CreatedAt time.Time
}

// ModelMeta returns metadata for API token persistence.
func (Token) ModelMeta() models.Metadata {
	return models.Metadata{
		AppLabel:          "api",
		ModelName:         "Token",
		TableName:         "api_token",
		DBTable:           "api_token",
		VerboseName:       "token",
		VerboseNamePlural: "tokens",
		Fields: []models.FieldMeta{
			{Name: "key", Column: "key", PrimaryKey: true},
			{Name: "user", Column: "user_id", RelationTarget: "auth.User", DeleteBehavior: "cascade"},
			{Name: "created_at", Column: "created_at"},
		},
		Constraints: []models.Constraint{
			{Name: "api_token_user_uniq", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("user_id")}},
		},
		DefaultPermissions: []string{"add", "change", "delete", "view"},
		DefaultManagerName: "objects",
		BaseManagerName:    "objects",
	}
}

// AuthenticationResult is the result of one successful authenticator.
type AuthenticationResult struct {
	User auth.User
	Auth any
}

// Authenticator authenticates one API request.
type Authenticator interface {
	Authenticate(context.Context, *Request) (AuthenticationResult, bool, error)
}

// AuthenticatorFunc adapts a function into an authenticator.
type AuthenticatorFunc func(context.Context, *Request) (AuthenticationResult, bool, error)

// Authenticate runs the function authenticator.
func (f AuthenticatorFunc) Authenticate(ctx context.Context, request *Request) (AuthenticationResult, bool, error) {
	return f(ctx, request)
}

// TokenStore looks up API tokens.
type TokenStore interface {
	FindToken(context.Context, string) (Token, bool, error)
}

// MemoryTokenStore is an in-memory token store for tests, examples, and bootstrapping.
type MemoryTokenStore struct {
	mu     sync.RWMutex
	tokens map[string]Token
}

// NewMemoryTokenStore creates an in-memory token store.
func NewMemoryTokenStore(tokens ...Token) *MemoryTokenStore {
	store := &MemoryTokenStore{tokens: map[string]Token{}}
	for _, token := range tokens {
		store.Add(token)
	}
	return store
}

// Add inserts or replaces a token.
func (s *MemoryTokenStore) Add(token Token) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token.Key] = cloneToken(token)
}

// FindToken returns a token by key.
func (s *MemoryTokenStore) FindToken(_ context.Context, key string) (Token, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	token, ok := s.tokens[key]
	return cloneToken(token), ok, nil
}

// AuthenticateRequest creates an APIView authentication lifecycle hook.
func AuthenticateRequest(authenticators ...Authenticator) RequestHook {
	return func(ctx context.Context, request *Request) error {
		request.WithUser(auth.AnonymousUser())
		for _, authenticator := range authenticators {
			if authenticator == nil {
				continue
			}
			result, ok, err := authenticator.Authenticate(ctx, request)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			request.WithUser(authenticatedUser(result.User)).WithAuth(result.Auth)
			return nil
		}
		return nil
	}
}

// SessionAuthentication authenticates users already attached by the auth middleware.
func SessionAuthentication() Authenticator {
	return AuthenticatorFunc(func(_ context.Context, request *Request) (AuthenticationResult, bool, error) {
		user, ok := auth.UserFromContext(request.Raw().Context())
		if !ok || !isAuthenticatedUser(user) {
			return AuthenticationResult{}, false, nil
		}
		return AuthenticationResult{User: authenticatedUser(user), Auth: "session"}, true, nil
	})
}

// TokenAuthentication authenticates Authorization: Token <key> requests.
func TokenAuthentication(store TokenStore) Authenticator {
	return AuthenticatorFunc(func(ctx context.Context, request *Request) (AuthenticationResult, bool, error) {
		key, ok, err := tokenFromAuthorizationHeader(request.Raw().Header.Get("Authorization"))
		if err != nil {
			return AuthenticationResult{}, false, err
		}
		if !ok {
			return AuthenticationResult{}, false, nil
		}
		if store == nil {
			return AuthenticationResult{}, false, ErrAuthenticationFailed
		}
		token, found, err := store.FindToken(ctx, key)
		if err != nil || !found {
			return AuthenticationResult{}, false, ErrAuthenticationFailed
		}
		user := token.User
		if user.ID == 0 {
			user.ID = token.UserID
		}
		if !user.IsActive {
			return AuthenticationResult{}, false, ErrAuthenticationFailed
		}
		return AuthenticationResult{User: authenticatedUser(user), Auth: token}, true, nil
	})
}

func tokenFromAuthorizationHeader(header string) (string, bool, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", false, nil
	}
	parts := strings.Fields(header)
	if len(parts) != 2 {
		return "", false, ErrAuthenticationFailed
	}
	scheme := strings.ToLower(parts[0])
	if scheme != "token" && scheme != "bearer" {
		return "", false, nil
	}
	key := strings.TrimSpace(parts[1])
	if key == "" {
		return "", false, ErrAuthenticationFailed
	}
	return key, true, nil
}

func authenticatedUser(user auth.User) auth.User {
	user.Authenticated = true
	user.Anonymous = false
	return user
}

func cloneToken(token Token) Token {
	token.User.Groups = append([]auth.Group(nil), token.User.Groups...)
	token.User.Permissions = append([]auth.Permission(nil), token.User.Permissions...)
	token.User.UserPermissions = append([]auth.Permission(nil), token.User.UserPermissions...)
	return token
}
