//go:build hotreload
// +build hotreload

package policy

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

// HotReloadManager manages hot reloading of policy configurations
type HotReloadManager struct {
	mu      sync.RWMutex
	engine  *Engine
	watcher *fsnotify.Watcher
	logger  *zap.Logger
	stopCh  chan struct{}
	running bool

	// Configuration
	debounceInterval time.Duration
	watchedFiles     map[string]bool
	lastReloadTime   time.Time

	// Callbacks
	onReloadSuccess []ReloadCallback
	onReloadError   []ReloadErrorCallback
}

// Note: ReloadCallback and ReloadErrorCallback are defined in interfaces.go

// NewHotReloadManager creates a new hot reload manager
func NewHotReloadManager(engine *Engine, logger *zap.Logger) (*HotReloadManager, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &HotReloadManager{
		engine:           engine,
		watcher:          watcher,
		logger:           logger,
		stopCh:           make(chan struct{}),
		debounceInterval: 500 * time.Millisecond, // Debounce file events
		watchedFiles:     make(map[string]bool),
		onReloadSuccess:  make([]ReloadCallback, 0),
		onReloadError:    make([]ReloadErrorCallback, 0),
	}, nil
}

// Start starts the hot reload monitoring
func (m *HotReloadManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return nil
	}

	// Add policy files to watcher
	if err := m.addPolicyFilesToWatcher(); err != nil {
		return err
	}

	m.running = true
	go m.watchLoop(ctx)

	m.logger.Info("Hot reload manager started",
		zap.Int("watched_files", len(m.watchedFiles)),
		zap.Duration("debounce_interval", m.debounceInterval))

	return nil
}

// Stop stops the hot reload monitoring
func (m *HotReloadManager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return nil
	}

	close(m.stopCh)
	m.running = false

	if err := m.watcher.Close(); err != nil {
		m.logger.Error("Error closing watcher", zap.Error(err))
		return err
	}

	m.logger.Info("Hot reload manager stopped")
	return nil
}

// AddReloadCallback adds a callback for successful reloads
func (m *HotReloadManager) AddReloadCallback(callback ReloadCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onReloadSuccess = append(m.onReloadSuccess, callback)
}

// AddReloadErrorCallback adds a callback for reload errors
func (m *HotReloadManager) AddReloadErrorCallback(callback ReloadErrorCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onReloadError = append(m.onReloadError, callback)
}

// watchLoop runs the main file watching loop
func (m *HotReloadManager) watchLoop(ctx context.Context) {
	debounceTimer := time.NewTimer(0)
	debounceTimer.Stop()

	var pendingReload bool

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case event := <-m.watcher.Events:
			m.logger.Debug("File event received",
				zap.String("file", event.Name),
				zap.String("op", event.Op.String()))

			if m.shouldProcessEvent(event) {
				// Debounce multiple rapid file events
				pendingReload = true
				debounceTimer.Reset(m.debounceInterval)
			}

		case err := <-m.watcher.Errors:
			m.logger.Error("File watcher error", zap.Error(err))

		case <-debounceTimer.C:
			if pendingReload {
				m.performReload()
				pendingReload = false
			}
		}
	}
}

// shouldProcessEvent determines if a file event should trigger a reload
func (m *HotReloadManager) shouldProcessEvent(event fsnotify.Event) bool {
	// Only process write and create events
	if event.Op&fsnotify.Write == 0 && event.Op&fsnotify.Create == 0 {
		return false
	}

	// Check if this is a watched file
	m.mu.RLock()
	defer m.mu.RUnlock()

	absPath, err := filepath.Abs(event.Name)
	if err != nil {
		return false
	}

	return m.watchedFiles[absPath]
}

// performReload reloads the policies
func (m *HotReloadManager) performReload() {
	startTime := time.Now()

	m.logger.Info("Starting policy reload due to file changes")

	// Reload policies using the engine
	if err := m.engine.ReloadPolicies(context.Background()); err != nil {
		m.logger.Error("Failed to reload policies", zap.Error(err))
		m.callErrorCallbacks(err, "")
		return
	}

	reloadDuration := time.Since(startTime)
	m.mu.Lock()
	m.lastReloadTime = time.Now()
	m.mu.Unlock()

	m.logger.Info("Policies reloaded successfully",
		zap.Duration("duration", reloadDuration),
		zap.Time("timestamp", m.lastReloadTime))

	// Call success callbacks
	m.callSuccessCallbacks(reloadDuration)
}

// addPolicyFilesToWatcher adds policy configuration files to the watcher
func (m *HotReloadManager) addPolicyFilesToWatcher() error {
	// Get the policy source from the engine's provider
	source := m.engine.provider.GetSource()

	// For file-based providers, add the file to the watcher
	if fileProvider, ok := m.engine.provider.(*FileBasedProvider); ok {
		absPath, err := filepath.Abs(fileProvider.configPath)
		if err != nil {
			return err
		}

		// Watch the file
		if err := m.watcher.Add(absPath); err != nil {
			return err
		}

		m.watchedFiles[absPath] = true

		// Also watch the directory (for atomic writes)
		dir := filepath.Dir(absPath)
		if err := m.watcher.Add(dir); err != nil {
			m.logger.Warn("Could not watch directory",
				zap.String("dir", dir),
				zap.Error(err))
		}

		m.logger.Debug("Added policy file to watcher",
			zap.String("file", absPath))
	}

	m.logger.Info("Policy files added to hot reload watcher",
		zap.String("source", source),
		zap.Int("files_watched", len(m.watchedFiles)))

	return nil
}

// callSuccessCallbacks calls all registered success callbacks
func (m *HotReloadManager) callSuccessCallbacks(duration time.Duration) {
	m.mu.RLock()
	callbacks := make([]ReloadCallback, len(m.onReloadSuccess))
	copy(callbacks, m.onReloadSuccess)
	m.mu.RUnlock()

	for _, callback := range callbacks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					m.logger.Error("Panic in reload success callback",
						zap.Any("panic", r))
				}
			}()
			callback(m.engine, duration)
		}()
	}
}

// callErrorCallbacks calls all registered error callbacks
func (m *HotReloadManager) callErrorCallbacks(err error, filePath string) {
	m.mu.RLock()
	callbacks := make([]ReloadErrorCallback, len(m.onReloadError))
	copy(callbacks, m.onReloadError)
	m.mu.RUnlock()

	for _, callback := range callbacks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					m.logger.Error("Panic in reload error callback",
						zap.Any("panic", r))
				}
			}()
			callback(err, filePath)
		}()
	}
}

// GetStatus returns the current hot reload status
func (m *HotReloadManager) GetStatus() *HotReloadStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return &HotReloadStatus{
		Running:           m.running,
		WatchedFilesCount: len(m.watchedFiles),
		LastReloadTime:    m.lastReloadTime,
		DebounceInterval:  m.debounceInterval,
	}
}

// Note: HotReloadStatus is defined in interfaces.go
