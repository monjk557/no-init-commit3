package connection

import (
	"bufio"
	"net"
	"sync"
	"sync/atomic"
)

// ioWrapper implements IOWrapper interface
type ioWrapper struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
	closed int32
	mu     sync.RWMutex

	// Optional progress callback
	progressCallback func(bytes int64)
}

// IOWrapperConfig configures the IO wrapper
type IOWrapperConfig struct {
	ReadBufferSize   int
	WriteBufferSize  int
	ProgressCallback func(bytes int64)
}

// DefaultIOWrapperConfig returns default configuration
// Uses centralized configuration manager
func DefaultIOWrapperConfig() *IOWrapperConfig {
	configManager := NewConfigManager()
	return configManager.ToIOWrapperConfig()
}

// IOWrapperConfigFromConfig creates IOWrapperConfig from ConfigManager
func IOWrapperConfigFromConfig(config *ConfigManager) *IOWrapperConfig {
	if config == nil {
		return DefaultIOWrapperConfig()
	}
	return config.ToIOWrapperConfig()
}

// NewIOWrapper creates a new IO wrapper
func NewIOWrapper(conn net.Conn, config *IOWrapperConfig) IOWrapper {
	if config == nil {
		config = DefaultIOWrapperConfig()
	}

	var reader *bufio.Reader
	var writer *bufio.Writer

	if config.ReadBufferSize > 0 {
		reader = bufio.NewReaderSize(conn, config.ReadBufferSize)
	} else {
		reader = bufio.NewReader(conn)
	}

	if config.WriteBufferSize > 0 {
		writer = bufio.NewWriterSize(conn, config.WriteBufferSize)
	} else {
		writer = bufio.NewWriter(conn)
	}

	return &ioWrapper{
		conn:             conn,
		reader:           reader,
		writer:           writer,
		progressCallback: config.ProgressCallback,
	}
}

// Read implements io.Reader with progress tracking
func (iw *ioWrapper) Read(p []byte) (int, error) {
	if atomic.LoadInt32(&iw.closed) == 1 {
		return 0, NewConnectionClosedError("read")
	}

	n, err := iw.reader.Read(p)
	if n > 0 && iw.progressCallback != nil {
		iw.progressCallback(int64(n))
	}

	return n, err
}

// Write implements io.Writer with progress tracking
func (iw *ioWrapper) Write(p []byte) (int, error) {
	if atomic.LoadInt32(&iw.closed) == 1 {
		return 0, NewConnectionClosedError("write")
	}

	n, err := iw.writer.Write(p)
	if n > 0 && iw.progressCallback != nil {
		iw.progressCallback(int64(n))
	}

	return n, err
}

// GetReader returns the buffered reader
func (iw *ioWrapper) GetReader() *bufio.Reader {
	iw.mu.RLock()
	defer iw.mu.RUnlock()
	return iw.reader
}

// GetWriter returns the buffered writer
func (iw *ioWrapper) GetWriter() *bufio.Writer {
	iw.mu.RLock()
	defer iw.mu.RUnlock()
	return iw.writer
}

// Flush flushes the write buffer
func (iw *ioWrapper) Flush() error {
	if atomic.LoadInt32(&iw.closed) == 1 {
		return NewConnectionClosedError("flush")
	}

	iw.mu.RLock()
	writer := iw.writer
	iw.mu.RUnlock()

	return writer.Flush()
}

// Close closes the IO wrapper
func (iw *ioWrapper) Close() error {
	if !atomic.CompareAndSwapInt32(&iw.closed, 0, 1) {
		return nil // Already closed
	}

	// Note: We don't close the underlying connection here
	// That's the responsibility of the ConnectionLifecycle
	return nil
}

// SetProgressCallback sets or updates the progress callback
func (iw *ioWrapper) SetProgressCallback(callback func(bytes int64)) {
	iw.mu.Lock()
	defer iw.mu.Unlock()
	iw.progressCallback = callback
}

// IsClosed returns whether the wrapper is closed
func (iw *ioWrapper) IsClosed() bool {
	return atomic.LoadInt32(&iw.closed) == 1
}
