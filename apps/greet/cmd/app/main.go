package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	space := os.Getenv("SPACE")

	slog.Info("greet app starting", "port", port, "space", space)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			name = "world"
		}
		fmt.Fprintf(w, "Hello, %s! (space: %s)\n", name, space)
	})

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
