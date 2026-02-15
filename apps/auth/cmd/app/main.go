package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/lmittmann/tint"
	"golang.org/x/oauth2"

	"github.com/housecat-inc/cheetah/apps/auth/pkg/templates"
)

type session struct {
	Email   string
	Name    string
	Picture string
}

func main() {
	space := os.Getenv("SPACE")
	slog.SetDefault(slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: slog.LevelInfo, TimeFormat: time.Kitchen})).With("app", space))

	port := os.Getenv("PORT")

	redirectURL := fmt.Sprintf("http://localhost:50000/auth/callback")

	oauthCfg := &oauth2.Config{
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
		RedirectURL: redirectURL,
		Scopes: []string{
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
	}

	var (
		mu       sync.RWMutex
		nonces   = map[string]bool{}
		sessions = map[string]*session{}
	)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil {
			templates.Login().Render(r.Context(), w)
			return
		}
		mu.RLock()
		sess, ok := sessions[cookie.Value]
		mu.RUnlock()
		if !ok {
			templates.Login().Render(r.Context(), w)
			return
		}
		templates.Profile(sess.Email, sess.Name, sess.Picture).Render(r.Context(), w)
	})

	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		nonce := generateNonce()
		mu.Lock()
		nonces[nonce] = true
		mu.Unlock()

		state := space + "|" + nonce
		http.Redirect(w, r, oauthCfg.AuthCodeURL(state), http.StatusTemporaryRedirect)
	})

	http.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")

		mu.Lock()
		valid := nonces[state]
		delete(nonces, state)
		mu.Unlock()

		if !valid {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}

		tok, err := oauthCfg.Exchange(r.Context(), r.URL.Query().Get("code"))
		if err != nil {
			slog.Error("token exchange failed", "error", err)
			http.Error(w, "token exchange failed", http.StatusInternalServerError)
			return
		}

		info, err := fetchUserInfo(tok.AccessToken)
		if err != nil {
			slog.Error("userinfo failed", "error", err)
			http.Error(w, "failed to get user info", http.StatusInternalServerError)
			return
		}

		sid := generateNonce()
		mu.Lock()
		sessions[sid] = info
		mu.Unlock()

		http.SetCookie(w, &http.Cookie{
			HttpOnly: true,
			Name:     "session",
			Path:     "/",
			SameSite: http.SameSiteLaxMode,
			Value:    sid,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	http.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("session"); err == nil {
			mu.Lock()
			delete(sessions, cookie.Value)
			mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{
			HttpOnly: true,
			MaxAge:   -1,
			Name:     "session",
			Path:     "/",
			Value:    "",
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	slog.Info("listening", "addr", ":"+port)
	if err := http.ListenAndServe(":"+port, requestLogger(http.DefaultServeMux)); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func fetchUserInfo(accessToken string) (*session, error) {
	resp, err := http.Get("https://www.googleapis.com/oauth2/v2/userinfo?access_token=" + url.QueryEscape(accessToken))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var info struct {
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &session{Email: info.Email, Name: info.Name, Picture: info.Picture}, nil
}

func generateNonce() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		slog.Info("request", "method", r.Method, "uri", r.URL.RequestURI(), "status", sw.status, "dur", time.Since(start).Round(time.Millisecond))
	})
}
