package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
	"github.com/lmittmann/tint"
)

func main() {
	space := os.Getenv("SPACE")
	slog.SetDefault(slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: slog.LevelInfo, TimeFormat: time.Kitchen})).With("app", space))

	port := os.Getenv("PORT")
	dbURL := os.Getenv("DATABASE_URL")

	if dbURL != "" {
		conn, err := sql.Open("postgres", dbURL)
		if err != nil {
			slog.Error("failed to connect to database", "error", err)
			os.Exit(1)
		}
		defer conn.Close()
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	slog.Info("listening", "addr", ":"+port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
