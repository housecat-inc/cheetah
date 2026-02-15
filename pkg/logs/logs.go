package logs

import (
	"log/slog"
	"os"
	"time"

	"github.com/lmittmann/tint"
)

func New(space string) *slog.Logger {
	return slog.New(tint.NewHandler(os.Stderr, &tint.Options{
		Level:      slog.LevelInfo,
		TimeFormat: time.Kitchen,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				return tint.Attr(6, slog.String(slog.LevelKey, "RUN"))
			}
			return a
		},
	})).With("space", space)
}
