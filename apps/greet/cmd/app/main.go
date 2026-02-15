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

	"github.com/housecat-inc/cheetah/apps/greet/pkg/db"
	"github.com/housecat-inc/cheetah/apps/greet/pkg/templates"
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

		var greetings []db.Greeting
		if queries != nil {
			var err error
			greetings, err = queries.ListGreetings(r.Context())
			if err != nil {
				slog.Error("failed to list greetings", "error", err)
			}
		}

		templates.Index(name, greetings).Render(r.Context(), w)
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
