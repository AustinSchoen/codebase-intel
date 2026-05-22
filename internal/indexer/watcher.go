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
// Used by the daemon as the per-codebase watcher; it watches the disk and
// ships changes to the server via the embedded Client.
type smartWatcher struct {
	client  *Client
	watcher *fsnotify.Watcher

	mu          sync.Mutex
	pending     map[string]fsnotify.Op // path -> accumulated ops
	eventCount  int                    // events in the current window
	windowStart time.Time
	bulkMode    bool
}

func newSmartWatcher(client *Client) (*smartWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &smartWatcher{
		client:      client,
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
	return filepath.WalkDir(sw.client.cfg.Codebase.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			for _, pattern := range sw.client.cfg.Codebase.ExcludePatterns {
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

	sw.client.logger.Println("smart watcher: watching for file changes...")

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
			sw.client.logger.Printf("watcher error: %v", err)

		case <-ticker.C:
			sw.processBatch(ctx)
		}
	}
}

// recordEvent accumulates an event and checks for branch switch conditions.
func (sw *smartWatcher) recordEvent(event fsnotify.Event) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}

	sw.pending[event.Name] |= event.Op

	now := time.Now()
	if now.Sub(sw.windowStart) > branchSwitchWindow {
		sw.eventCount = 0
		sw.windowStart = now
	}
	sw.eventCount++

	if sw.eventCount >= branchSwitchThreshold && !sw.bulkMode {
		sw.bulkMode = true
		sw.client.logger.Printf("smart watcher: branch switch detected (%d events in window), entering bulk sync mode", sw.eventCount)
	}
}

// processBatch processes accumulated events. In bulk mode (after detecting a
// branch switch), it triggers a full differential re-index; in normal mode it
// processes individual file events by uploading or deleting on the server.
func (sw *smartWatcher) processBatch(ctx context.Context) {
	sw.mu.Lock()
	if len(sw.pending) == 0 {
		sw.mu.Unlock()
		return
	}

	if sw.bulkMode {
		sw.pending = make(map[string]fsnotify.Op)
		sw.bulkMode = false
		sw.eventCount = 0
		sw.mu.Unlock()

		sw.client.logger.Println("smart watcher: waiting for disk to settle before bulk sync...")
		time.Sleep(branchSwitchSettleTime)

		sw.client.logger.Println("smart watcher: running full differential re-index")
		if err := sw.client.FullIndex(ctx); err != nil {
			sw.client.logger.Printf("smart watcher: bulk sync error: %v", err)
		}
		return
	}

	batch := sw.pending
	sw.pending = make(map[string]fsnotify.Op)
	sw.mu.Unlock()

	for path, op := range batch {
		if op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			rel, _ := filepath.Rel(sw.client.cfg.Codebase.Path, path)
			sw.client.logger.Printf("file removed: %s", rel)
			if err := sw.client.DeleteFile(ctx, path); err != nil {
				sw.client.logger.Printf("error deleting %s on server: %v", rel, err)
			}
			continue
		}

		if op&(fsnotify.Write|fsnotify.Create) != 0 {
			lang := sw.client.DetectLanguage(path)
			if lang != "" {
				sw.client.logger.Printf("file changed: %s", path)
				if err := sw.client.IndexSingleFile(ctx, path); err != nil {
					sw.client.logger.Printf("error uploading %s: %v", path, err)
				}
			}
		}
	}
}
