package watch

// Standalone file watcher inspired by esbuild's polling watcher.
// Uses randomized scan order with a recent-items fast path to balance
// CPU usage against change detection latency.

import (
	"bytes"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	watchIntervalSleep       = 100 * time.Millisecond
	maxRecentItemCount       = 16
	minItemCountPerIter      = 64
	maxIntervalsBeforeUpdate = 20
	rescanInterval           = 50 // ~5 seconds between full rescans
)

type Watcher struct {
	dir            string
	patterns       []string
	ignorePatterns []string
	onChange       func(path string)

	modTimes          map[string]time.Time
	recentItems       []string
	itemsToScan       []string
	itemsPerIteration int
	shouldStop        int32
	mutex             sync.Mutex
	stopWaitGroup     sync.WaitGroup
	logger            *slog.Logger
}

func New(dir string, patterns, ignorePatterns []string, onChange func(path string)) *Watcher {
	return &Watcher{
		dir:            dir,
		patterns:       patterns,
		ignorePatterns: ignorePatterns,
		onChange:        onChange,
		logger:         slog.Default(),
	}
}

func (w *Watcher) Start() {
	w.modTimes = w.Scan()
	w.logger.Info("watch", "dir", w.dir, "files", len(w.modTimes))

	w.stopWaitGroup.Add(1)
	go func() {
		defer w.stopWaitGroup.Done()
		rescanCounter := 0
		for atomic.LoadInt32(&w.shouldStop) == 0 {
			time.Sleep(watchIntervalSleep)

			rescanCounter++
			if rescanCounter >= rescanInterval {
				w.RefreshFileList()
				rescanCounter = 0
			}

			if dirtyPath := w.tryToFindDirtyPath(); dirtyPath != "" {
				w.onChange(dirtyPath)
			}
		}
	}()
}

func (w *Watcher) Stop() {
	atomic.StoreInt32(&w.shouldStop, 1)
	w.stopWaitGroup.Wait()
}

func (w *Watcher) Scan() map[string]time.Time {
	result := make(map[string]time.Time)
	filepath.WalkDir(w.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			if MatchesAny(name, w.ignorePatterns) {
				return filepath.SkipDir
			}
			return nil
		}
		relPath, _ := filepath.Rel(w.dir, path)
		if len(w.patterns) > 0 && !MatchesAny(relPath, w.patterns) {
			return nil
		}
		if MatchesAny(relPath, w.ignorePatterns) {
			return nil
		}
		if hasDoNotEdit(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		result[path] = info.ModTime()
		return nil
	})
	return result
}

func (w *Watcher) RefreshFileList() {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	fresh := w.Scan()

	// Remove deleted files
	for path := range w.modTimes {
		if _, ok := fresh[path]; !ok {
			delete(w.modTimes, path)
		}
	}
	// Add new files
	for path, modTime := range fresh {
		if _, ok := w.modTimes[path]; !ok {
			w.modTimes[path] = modTime
		}
	}

	// Reset scan list so it gets rebuilt on next tryToFindDirtyPath
	w.itemsToScan = w.itemsToScan[:0]
}

func (w *Watcher) tryToFindDirtyPath() string {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	// Rebuild scan list if empty
	if len(w.itemsToScan) == 0 {
		items := w.itemsToScan[:0]
		for path := range w.modTimes {
			items = append(items, path)
		}
		rand.Shuffle(len(items), func(i, j int) {
			items[i], items[j] = items[j], items[i]
		})
		w.itemsToScan = items

		perIter := (len(items) + maxIntervalsBeforeUpdate - 1) / maxIntervalsBeforeUpdate
		if perIter < minItemCountPerIter {
			perIter = minItemCountPerIter
		}
		w.itemsPerIteration = perIter
	}

	// Always check recent items first
	for i, path := range w.recentItems {
		if w.isDirty(path) {
			copy(w.recentItems[i:], w.recentItems[i+1:])
			w.recentItems[len(w.recentItems)-1] = path
			return path
		}
	}

	// Check a batch from the scan list
	remainingCount := len(w.itemsToScan) - w.itemsPerIteration
	if remainingCount < 0 {
		remainingCount = 0
	}
	toCheck := w.itemsToScan[remainingCount:]
	w.itemsToScan = w.itemsToScan[:remainingCount]

	for _, path := range toCheck {
		if w.isDirty(path) {
			w.recentItems = append(w.recentItems, path)
			if len(w.recentItems) > maxRecentItemCount {
				copy(w.recentItems, w.recentItems[1:])
				w.recentItems = w.recentItems[:maxRecentItemCount]
			}
			return path
		}
	}

	return ""
}

func (w *Watcher) isDirty(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		// File was deleted
		delete(w.modTimes, path)
		return true
	}
	if info.ModTime().After(w.modTimes[path]) {
		w.modTimes[path] = info.ModTime()
		return true
	}
	return false
}

var doNotEditMarker = []byte("DO NOT EDIT")

func hasDoNotEdit(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 1024)
	n, err := io.ReadAtLeast(f, buf, 1)
	if err != nil {
		return false
	}
	return bytes.Contains(buf[:n], doNotEditMarker)
}

func MatchesAny(path string, patterns []string) bool {
	name := filepath.Base(path)
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, path); matched {
			return true
		}
	}
	return false
}
