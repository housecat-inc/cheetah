package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
	"github.com/lmittmann/tint"

	"github.com/housecat-inc/spacecat/apps/greet/internal/db"
)

func main() {
	space := os.Getenv("SPACE")
	slog.SetDefault(slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: slog.LevelInfo, TimeFormat: time.Kitchen})).With("app", space))

	port := os.Getenv("PORT")
	dbURL := os.Getenv("DATABASE_URL")

	slog.Info("greet app starting", "port", port)

	var queries *db.Queries
	if dbURL != "" {
		conn, err := sql.Open("postgres", dbURL)
		if err != nil {
			slog.Error("failed to connect to database", "error", err)
			os.Exit(1)
		}
		defer conn.Close()
		queries = db.New(conn)
		slog.Info("database connected", "url", dbURL)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			name = "world"
		}

		var greetingsHTML string
		if queries != nil {
			greetings, err := queries.ListGreetings(r.Context())
			if err == nil {
				for _, g := range greetings {
					greetingsHTML += fmt.Sprintf(
						`<li>%s <strong>%s</strong>: %s <small>(%s)</small></li>`,
						g.Emoji, g.Name, g.Message, g.CreatedAt.Format("15:04:05"),
					)
				}
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Greet</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 600px; margin: 2rem auto; padding: 0 1rem; }
  form { display: flex; gap: 0.5rem; margin-bottom: 1.5rem; }
  input { padding: 0.4rem 0.6rem; border: 1px solid #ccc; border-radius: 4px; }
  input[name="emoji"] { width: 3rem; text-align: center; }
  button { padding: 0.4rem 1rem; border-radius: 4px; border: 1px solid #ccc; cursor: pointer; }
  ul { list-style: none; padding: 0; }
  li { padding: 0.4rem 0; border-bottom: 1px solid #eee; }
  small { color: #888; }
</style>
</head>
<body>
<h1>Hello, %s</h1>
<h2>Leave a greeting</h2>
<form method="POST" action="/greetings">
  <input name="name" placeholder="Your name" required>
  <input name="message" placeholder="Message" required>
  <input name="emoji" placeholder="Emoji" value="&#x1F44B;">
  <button type="submit">Send</button>
</form>
<h2>Recent greetings</h2>
<ul>%s</ul>
</body></html>`, name, greetingsHTML)
	})

	http.HandleFunc("/greetings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if queries == nil {
				http.Error(w, "database not configured", http.StatusServiceUnavailable)
				return
			}
			r.ParseForm()
			emoji := r.FormValue("emoji")
			if emoji == "" {
				emoji = "👋"
			}
			g, err := queries.CreateGreeting(r.Context(), db.CreateGreetingParams{
				Name:    r.FormValue("name"),
				Message: r.FormValue("message"),
				Emoji:   emoji,
			})
			if err != nil {
				slog.Error("failed to create greeting", "error", err)
				http.Error(w, "failed to create greeting", http.StatusInternalServerError)
				return
			}

			// If form submission, redirect back
			if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(g)
			return
		}

		if queries == nil {
			json.NewEncoder(w).Encode([]any{})
			return
		}
		greetings, err := queries.ListGreetings(r.Context())
		if err != nil {
			slog.Error("failed to list greetings", "error", err)
			http.Error(w, "failed to list greetings", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(greetings)
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
