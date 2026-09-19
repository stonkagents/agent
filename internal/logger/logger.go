// Package: internal/logger
// Feature: F-009 (CLI Tool)
// Story: US-009-01 (CLI Tool Scaffolding)
// Purpose: Structured logging with rotation support

package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger provides structured logging with file rotation
type Logger struct {
	mu          sync.Mutex
	file        *os.File
	logger      *log.Logger
	logPath     string
	maxSize     int64 // Max size in bytes before rotation
	maxBackups  int   // Number of old log files to keep
	currentSize int64
}

// Config holds logger configuration
type Config struct {
	LogPath    string // Path to log file
	MaxSize    int64  // Max size in bytes (default: 10MB)
	MaxBackups int    // Number of backups to keep (default: 5)
}

// New creates a new logger instance
func New(cfg Config) (*Logger, error) {
	// Set defaults
	if cfg.MaxSize == 0 {
		cfg.MaxSize = 10 * 1024 * 1024 // 10MB
	}
	if cfg.MaxBackups == 0 {
		cfg.MaxBackups = 5
	}

	// Ensure log directory exists
	dir := filepath.Dir(cfg.LogPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Open log file
	file, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	// Get current file size
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to stat log file: %w", err)
	}

	l := &Logger{
		file:        file,
		logger:      log.New(file, "", 0), // No prefix, we'll format ourselves
		logPath:     cfg.LogPath,
		maxSize:     cfg.MaxSize,
		maxBackups:  cfg.MaxBackups,
		currentSize: stat.Size(),
	}

	return l, nil
}

// Info logs an informational message
func (l *Logger) Info(service, method, message string, fields map[string]interface{}) {
	l.log("INFO", service, method, message, fields)
}

// Warn logs a warning message
func (l *Logger) Warn(service, method, message string, fields map[string]interface{}) {
	l.log("WARN", service, method, message, fields)
}

// Error logs an error message
func (l *Logger) Error(service, method, message string, fields map[string]interface{}) {
	l.log("ERROR", service, method, message, fields)
}

// Debug logs a debug message (only in dev mode)
func (l *Logger) Debug(service, method, message string, fields map[string]interface{}) {
	// For now, always log debug (production would check env var)
	l.log("DEBUG", service, method, message, fields)
}

// log writes a structured log entry
func (l *Logger) log(level, service, method, message string, fields map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Format: 2006-01-02T15:04:05Z [LEVEL] [Service.method] message field1=value1 field2=value2
	timestamp := time.Now().UTC().Format(time.RFC3339)
	logLine := fmt.Sprintf("%s [%s] [%s.%s] %s", timestamp, level, service, method, message)

	// Append fields
	if len(fields) > 0 {
		for k, v := range fields {
			logLine += fmt.Sprintf(" %s=%v", k, v)
		}
	}

	logLine += "\n"

	// Write to file
	n, err := l.file.WriteString(logLine)
	if err != nil {
		// Fallback to stderr if file write fails
		fmt.Fprintf(os.Stderr, "Failed to write to log file: %v\n", err)
		fmt.Fprint(os.Stderr, logLine)
		return
	}

	l.currentSize += int64(n)

	// Check if rotation is needed
	if l.currentSize >= l.maxSize {
		l.rotate()
	}
}

// rotate performs log file rotation
func (l *Logger) rotate() {
	// Close current file
	l.file.Close()

	// Rename old files
	for i := l.maxBackups - 1; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", l.logPath, i)
		newPath := fmt.Sprintf("%s.%d", l.logPath, i+1)
		os.Rename(oldPath, newPath) // Ignore errors, file might not exist
	}

	// Rename current log to .1
	os.Rename(l.logPath, l.logPath+".1")

	// Open new log file
	file, err := os.OpenFile(l.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open new log file: %v\n", err)
		return
	}

	l.file = file
	l.logger.SetOutput(file)
	l.currentSize = 0
}

// Close closes the log file
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

// MultiWriter returns an io.Writer that writes to both the log file and stdout
func (l *Logger) MultiWriter() io.Writer {
	return io.MultiWriter(l.file, os.Stdout)
}
