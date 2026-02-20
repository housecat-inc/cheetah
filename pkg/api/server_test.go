package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func TestIsOAuthCallback(t *testing.T) {
	tests := []struct {
		_name string
		out   bool
		url   string
	}{
		{
			_name: "auth callback",
			out:   true,
			url:   "/auth/callback?code=abc&state=nonce",
		},
		{
			_name: "provider callback",
			out:   true,
			url:   "/auth/google/callback?code=abc&state=nonce",
		},
		{
			_name: "connections callback",
			out:   true,
			url:   "/connections/gmail/callback?code=abc&state=nonce",
		},
		{
			_name: "missing code param",
			out:   false,
			url:   "/auth/callback?state=nonce",
		},
		{
			_name: "missing state param",
			out:   false,
			url:   "/auth/callback?code=abc",
		},
		{
			_name: "no callback in path",
			out:   false,
			url:   "/auth/login?code=abc&state=nonce",
		},
		{
			_name: "regular page",
			out:   false,
			url:   "/dashboard",
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			a.Equal(tt.out, isOAuthCallback(req))
		})
	}
}

func TestHandleOAuthBounce(t *testing.T) {
	tests := []struct {
		_name      string
		location   string
		oauthState map[string]string
		out        int
		path       string
		query      string
		register   []string
	}{
		{
			_name:    "redirects to active app",
			location: "http://auth.localhost:50000/auth/callback?code=abc&state=nonce123",
			out:      http.StatusTemporaryRedirect,
			path:     "/auth/callback",
			query:    "code=abc&state=nonce123",
			register: []string{"auth"},
		},
		{
			_name:    "preserves provider path",
			location: "http://auth.localhost:50000/auth/google/callback?code=abc&state=nonce123",
			out:      http.StatusTemporaryRedirect,
			path:     "/auth/google/callback",
			query:    "code=abc&state=nonce123",
			register: []string{"auth"},
		},
		{
			_name:    "connections callback",
			location: "http://auth.localhost:50000/connections/gmail/callback?code=abc&state=nonce123",
			out:      http.StatusTemporaryRedirect,
			path:     "/connections/gmail/callback",
			query:    "code=abc&state=nonce123",
			register: []string{"auth"},
		},
		{
			_name: "no app registered",
			out:   http.StatusBadGateway,
			path:  "/auth/callback",
			query: "code=abc&state=nonce123",
		},
		{
			_name:      "routes to app that initiated oauth",
			location:   "http://buffalo.localhost:50000/auth/callback?code=abc&state=xyz789",
			oauthState: map[string]string{"xyz789": "buffalo"},
			out:        http.StatusTemporaryRedirect,
			path:       "/auth/callback",
			query:      "code=abc&state=xyz789",
			register:   []string{"buffalo", "manama"},
		},
		{
			_name:    "falls back to active app for unknown state",
			location: "http://manama.localhost:50000/auth/callback?code=abc&state=unknown",
			out:      http.StatusTemporaryRedirect,
			path:     "/auth/callback",
			query:    "code=abc&state=unknown",
			register: []string{"buffalo", "manama"},
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			srv := NewServer(ServerConfig{
				BluePortStart: 4000,
				DashboardPort: 50000,
				PostgresPort:  54320,
			}, slog.Default())

			for _, space := range tt.register {
				srv.register(AppIn{Space: space, Dir: t.TempDir()})
			}

			for state, space := range tt.oauthState {
				srv.oauthStates.Store(state, space)
			}

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s?%s", tt.path, tt.query), nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := srv.handleOAuthBounce(c)
			a.NoError(err)
			a.Equal(tt.out, rec.Code)

			if tt.location != "" {
				a.Equal(tt.location, rec.Header().Get("Location"))
			}
		})
	}
}

func TestOAuthStateConsumedAfterUse(t *testing.T) {
	a := assert.New(t)

	srv := NewServer(ServerConfig{
		BluePortStart: 4000,
		DashboardPort: 50000,
		PostgresPort:  54320,
	}, slog.Default())
	srv.register(AppIn{Space: "buffalo", Dir: t.TempDir()})
	srv.register(AppIn{Space: "manama", Dir: t.TempDir()})
	srv.oauthStates.Store("once123", "buffalo")

	e := echo.New()

	req1 := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state=once123", nil)
	rec1 := httptest.NewRecorder()
	a.NoError(srv.handleOAuthBounce(e.NewContext(req1, rec1)))
	a.Equal(http.StatusTemporaryRedirect, rec1.Code)
	a.Contains(rec1.Header().Get("Location"), "buffalo.localhost")

	req2 := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state=once123", nil)
	rec2 := httptest.NewRecorder()
	a.NoError(srv.handleOAuthBounce(e.NewContext(req2, rec2)))
	a.Equal(http.StatusTemporaryRedirect, rec2.Code)
	a.Contains(rec2.Header().Get("Location"), "manama.localhost")
}
