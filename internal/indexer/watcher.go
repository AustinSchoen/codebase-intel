package indexer

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	batchInterval          = 2 * time.Second
	branchSwitchThreshold  = 500
	branchSwitchWindow     = 60 * time.Second
	branchSwitchSettleTime = 5 * time.Second
)

// smartWatcher wraps fsnotify with event batching and branch switch detection.
type smartWatcher struct {
	idx     *Indexer
	watcher *fsnotify.Watcher

	mu          sync.Mutex
	pending     map[string]fsnotify.Op // path -> accumulated ops
	eventCount  int                    // events in the current window
	windowStart time.Time
	bulkMode    bool
}

func newSmartWatcher(idx *Indexer) (*smartWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &smartWatcher{
		idx:         idx,
		watcher:     w,
		pending:     make(map[string]fsnotify.Op),
		windowStart: time.Now(),
	}, nil
}

func (sw *smartWatcher) close() {
	sw.watcher.Close()
}

// addDirectories recursively adds all non-excluded directories to the watcher.
func (sw *smartWatcher) addDirectories() error {
	return filepath.WalkDir(sw.idx.cfg.Codebase.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			for _, pattern := range sw.idx.cfg.Codebase.ExcludePatterns {
				trimmed := strings.TrimPrefix(pattern, "**/")
				if matched, _ := filepath.Match(trimmed, filepath.Base(path)); matched {
					return filepath.SkipDir
				}
			}
			return sw.watcher.Add(path)
		}
		return nil
	})
}

// run is the main event loop with batching and branch switch detection.
func (sw *smartWatcher) run(ctx context.Context) error {
	if err := sw.addDirectories(); err != nil {
		return err
	}

	sw.idx.logger.Println("smart watcher: watching for file changes...")

	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case event, ok := <-sw.watcher.Events:
			if !ok {
				return nil
			}
			sw.recordEvent(event)

		case err, ok := <-sw.watcher.Errors:
			if !ok {
				return nil
			}
			sw.idx.logger.Printf("watcher error: %v", err)

		case <-ticker.C:
			sw.processBatch(ctx)
		}
	}
}

// recordEvent accumulates an event and checks for branch switch conditions.
func (sw *smartWatcher) recordEvent(event fsnotify.Event) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	// Only track relevant ops on supported file types
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}

	// Accumulate ops
	sw.pending[event.Name] |= event.Op

	// Track event rate for branch switch detection
	now := time.Now()
	if now.Sub(sw.windowStart) > branchSwitchWindow {
		sw.eventCount = 0
		sw.windowStart = now
	}
	sw.eventCount++

	if sw.eventCount >= branchSwitchThreshold && !sw.bulkMode {
		sw.bulkMode = true
		sw.idx.logger.Printf("smart watcher: branch switch detected (%d events in window), entering bulk sync mode", sw.eventCount)
	}
}

// processBatch processes accumulated events, handling bulk mode differently.
func (sw *smartWatcher) processBatch(ctx context.Context) {
	sw.mu.Lock()
	if len(sw.pending) == 0 {
		sw.mu.Unlock()
		return
	}

	if sw.bulkMode {
		// Clear pending events — we'll do a full differential re-index
		sw.pending = make(map[string]fsnotify.Op)
		sw.bulkMode = false
		sw.eventCount = 0
		sw.mu.Unlock()

		sw.idx.logger.Println("smart watcher: waiting for disk to settle before bulk sync...")
		time.Sleep(branchSwitchSettleTime)

		sw.idx.logger.Println("smart watcher: running full differential re-index")
		if err := sw.idx.fullIndex(ctx); err != nil {
			sw.idx.logger.Printf("smart watcher: bulk sync error: %v", err)
		}
		return
	}

	// Normal mode: process individual events
	batch := sw.pending
	sw.pending = make(map[string]fsnotify.Op)
	sw.mu.Unlock()

	for path, op := range batch {
		if op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			relPath, _ := filepath.Rel(sw.idx.cfg.Codebase.Path, path)
			sw.idx.logger.Printf("file removed: %s", relPath)
			if err := sw.idx.store.DeleteFileData(ctx, sw.idx.cfg.Codebase.Name, relPath); err != nil {
				sw.idx.logger.Printf("error cleaning up %s: %v", relPath, err)
			}
			if err := sw.idx.qdrant.DeleteByFilter(ctx, sw.idx.cfg.Codebase.Name, relPath); err != nil {
				sw.idx.logger.Printf("error deleting vectors for %s: %v", relPath, err)
			}
			continue
		}

		if op&(fsnotify.Write|fsnotify.Create) != 0 {
			lang := sw.idx.detectLanguage(path)
			if lang != "" {
				sw.idx.logger.Printf("file changed: %s", path)
				if _, _, err := sw.idx.indexFile(ctx, path); err != nil {
					sw.idx.logger.Printf("error re-indexing %s: %v", path, err)
				}
			}
		}
	}
}
