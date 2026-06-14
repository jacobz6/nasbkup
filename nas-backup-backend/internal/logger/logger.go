// Package logger provides structured, leveled logging for the NAS backup system.
// It writes to both stdout and an optional log file with rotation support.
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

// Level represents log severity.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// String returns a human-readable label for the log level.
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Logger wraps the standard log with level filtering and dual output.
type Logger struct {
	mu        sync.Mutex
	level     Level
	logger    *log.Logger
	file      *os.File
	maxSizeMB int
	maxFiles  int
	filePath  string
}

// global is the package-level singleton logger.
var global *Logger

// Init initializes the global logger with the given configuration.
func Init(level string, filePath string, maxSizeMB, maxFiles int) error {
	lvl := parseLevel(level)

	l := &Logger{
		level:     lvl,
		maxSizeMB: maxSizeMB,
		maxFiles:  maxFiles,
		filePath:  filePath,
	}

	writers := []io.Writer{os.Stdout}

	if filePath != "" {
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return fmt.Errorf("create log directory: %w", err)
		}

		f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("open log file %s: %w", filePath, err)
		}
		l.file = f
		writers = append(writers, f)
	}

	l.logger = log.New(io.MultiWriter(writers...), "", 0)
	global = l

	return nil
}

// Close closes the log file if open.
func Close() {
	if global != nil && global.file != nil {
		global.file.Close()
	}
}

// Debug logs a debug-level message.
func Debug(format string, args ...interface{}) {
	global.logf(LevelDebug, format, args...)
}

// Info logs an info-level message.
func Info(format string, args ...interface{}) {
	global.logf(LevelInfo, format, args...)
}

// Warn logs a warning-level message.
func Warn(format string, args ...interface{}) {
	global.logf(LevelWarn, format, args...)
}

// Error logs an error-level message.
func Error(format string, args ...interface{}) {
	global.logf(LevelError, format, args...)
}

// logf writes a formatted log entry if the level is at or above the threshold.
func (l *Logger) logf(level Level, format string, args ...interface{}) {
	if level < l.level {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Check rotation before writing.
	if l.filePath != "" && l.file != nil {
		l.rotateIfNeeded()
	}

	ts := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf(format, args...)
	l.logger.Printf("[%s] %s %s", ts, level.String(), msg)
}

// rotateIfNeeded checks if the log file exceeds the size limit and rotates.
func (l *Logger) rotateIfNeeded() {
	if l.file == nil || l.maxSizeMB <= 0 {
		return
	}

	info, err := l.file.Stat()
	if err != nil {
		return
	}

	if info.Size() < int64(l.maxSizeMB)*1024*1024 {
		return
	}

	// Close current file.
	l.file.Close()

	// Shift rotated files: .log.3 -> delete, .log.2 -> .log.3, etc.
	for i := l.maxFiles - 1; i >= 1; i-- {
		older := fmt.Sprintf("%s.%d", l.filePath, i)
		newer := fmt.Sprintf("%s.%d", l.filePath, i-1)
		if i == l.maxFiles-1 {
			os.Remove(older)
		}
		if _, err := os.Stat(newer); err == nil {
			os.Rename(newer, older)
		}
	}

	// Rotate current file to .log.0.
	os.Rename(l.filePath, l.filePath+".0")

	// Open a fresh log file.
	f, err := os.OpenFile(l.filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		// Fallback to stdout only.
		l.logger.SetOutput(os.Stdout)
		l.file = nil
		return
	}
	l.file = f
	l.logger.SetOutput(io.MultiWriter(os.Stdout, f))
}

// parseLevel converts a level string to a Level value.
func parseLevel(s string) Level {
	switch s {
	case "debug", "DEBUG":
		return LevelDebug
	case "info", "INFO":
		return LevelInfo
	case "warn", "WARN", "warning", "WARNING":
		return LevelWarn
	case "error", "ERROR":
		return LevelError
	default:
		return LevelInfo
	}
}
