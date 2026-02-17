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

func TestHandleOAuthBounce(t *testing.T) {
	tests := []struct {
		_name    string
		query    string
		register string
		out      int
		location string
	}{
		{
			_name:    "redirects to active app",
			query:    "code=abc&state=nonce123",
			register: "auth",
			out:      http.StatusTemporaryRedirect,
			location: "http://auth.localhost:50000/auth/callback?code=abc&state=nonce123",
		},
		{
			_name: "no app registered",
			query: "code=abc&state=nonce123",
			out:   http.StatusBadGateway,
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

			if tt.register != "" {
				srv.register(AppIn{Space: tt.register, Dir: t.TempDir()})
			}

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/auth/callback?%s", tt.query), nil)
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
