package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Newton-School/gogo/auth"
)

func TestAnonymousThrottleUsesIPAndRateWindows(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryThrottleStore()
	throttle := AnonymousRateThrottle(Rate{Limit: 2, Window: time.Minute}, store)
	throttle.Now = func() time.Time { return now }
	request := NewRequest(httptest.NewRequest(http.MethodGet, "/api/", nil))
	request.Raw().RemoteAddr = "192.0.2.1:1234"

	if err := CheckThrottles(throttle)(context.Background(), request); err != nil {
		t.Fatalf("first throttle error = %v", err)
	}
	if err := CheckThrottles(throttle)(context.Background(), request); err != nil {
		t.Fatalf("second throttle error = %v", err)
	}
	err := CheckThrottles(throttle)(context.Background(), request)
	var throttleErr *ThrottleError
	if !errors.As(err, &throttleErr) || !errors.Is(err, ErrThrottled) || throttleErr.RetryAfter <= 0 {
		t.Fatalf("third throttle error = %v", err)
	}

	otherIP := NewRequest(httptest.NewRequest(http.MethodGet, "/api/", nil))
	otherIP.Raw().RemoteAddr = "192.0.2.2:1234"
	if err := CheckThrottles(throttle)(context.Background(), otherIP); err != nil {
		t.Fatalf("other ip throttle error = %v", err)
	}

	now = now.Add(time.Minute)
	if err := CheckThrottles(throttle)(context.Background(), request); err != nil {
		t.Fatalf("after window throttle error = %v", err)
	}
}

func TestAuthenticatedAndScopedThrottlesUseUserAndScopeKeys(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryThrottleStore()
	userThrottle := UserRateThrottle(Rate{Limit: 1, Window: time.Minute}, store)
	userThrottle.Now = func() time.Time { return now }
	scopeA := ScopedRateThrottle("reports", Rate{Limit: 1, Window: time.Minute}, store)
	scopeA.Now = func() time.Time { return now }
	scopeB := ScopedRateThrottle("exports", Rate{Limit: 1, Window: time.Minute}, store)
	scopeB.Now = func() time.Time { return now }

	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 7, IsActive: true, Authenticated: true}}}
	request := NewRequest(httptest.NewRequest(http.MethodGet, "/api/", nil)).WithUser(user)
	if err := CheckThrottles(userThrottle)(context.Background(), request); err != nil {
		t.Fatalf("user first throttle error = %v", err)
	}
	if err := CheckThrottles(userThrottle)(context.Background(), request); !errors.Is(err, ErrThrottled) {
		t.Fatalf("user second throttle error = %v, want ErrThrottled", err)
	}

	otherUser := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 8, IsActive: true, Authenticated: true}}}
	otherRequest := NewRequest(httptest.NewRequest(http.MethodGet, "/api/", nil)).WithUser(otherUser)
	if err := CheckThrottles(userThrottle)(context.Background(), otherRequest); err != nil {
		t.Fatalf("other user throttle error = %v", err)
	}

	if err := CheckThrottles(scopeA)(context.Background(), request); err != nil {
		t.Fatalf("scope A first throttle error = %v", err)
	}
	if err := CheckThrottles(scopeA)(context.Background(), request); !errors.Is(err, ErrThrottled) {
		t.Fatalf("scope A second throttle error = %v, want ErrThrottled", err)
	}
	if err := CheckThrottles(scopeB)(context.Background(), request); err != nil {
		t.Fatalf("scope B throttle error = %v", err)
	}
}

func TestThrottleRetryAfterHeader(t *testing.T) {
	response := DefaultExceptionHandler(context.Background(), nil, &ThrottleError{RetryAfter: 3 * time.Second})
	if response.status != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "3" {
		t.Fatalf("throttle response = %#v retry-after=%q", response, response.Header().Get("Retry-After"))
	}
}
