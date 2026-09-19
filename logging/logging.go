package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Level represents the severity of a log message.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// String returns the human-readable name of the log level.
func (level Level) String() string {
	switch level {
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

// ParseLevel converts a string to a Level. Returns LevelInfo for unrecognized values.
func ParseLevel(levelString string) Level {
	switch levelString {
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

// Format controls the output encoding of log messages.
type Format int

const (
	FormatHuman Format = iota
	FormatJSON
)

// Logger writes structured log messages to a destination writer.
type Logger struct {
	output    io.Writer
	level     Level
	format    Format
	component string
	mutex     sync.Mutex
}

// jsonEntry is the JSON-serialized form of a single log line.
type jsonEntry struct {
	Timestamp string            `json:"ts"`
	Level     string            `json:"level"`
	Component string            `json:"component,omitempty"`
	Message   string            `json:"msg"`
	Fields    map[string]string `json:"fields,omitempty"`
}

var defaultLogger = &Logger{
	output: os.Stderr,
	level:  LevelInfo,
	format: FormatHuman,
}

// Default returns the process-wide default logger.
func Default() *Logger {
	return defaultLogger
}

// SetDefault replaces the process-wide default logger.
func SetDefault(logger *Logger) {
	defaultLogger = logger
}

// New creates a logger with the given configuration.
func New(output io.Writer, level Level, format Format) *Logger {
	return &Logger{
		output: output,
		level:  level,
		format: format,
	}
}

// WithComponent returns a new logger that tags every message with the given component name.
func (logger *Logger) WithComponent(component string) *Logger {
	return &Logger{
		output:    logger.output,
		level:     logger.level,
		format:    logger.format,
		component: component,
	}
}

// Debug logs a message at debug level.
func (logger *Logger) Debug(message string, keyValues ...string) {
	logger.log(LevelDebug, message, keyValues)
}

// Info logs a message at info level.
func (logger *Logger) Info(message string, keyValues ...string) {
	logger.log(LevelInfo, message, keyValues)
}

// Warn logs a message at warn level.
func (logger *Logger) Warn(message string, keyValues ...string) {
	logger.log(LevelWarn, message, keyValues)
}

// Error logs a message at error level.
func (logger *Logger) Error(message string, keyValues ...string) {
	logger.log(LevelError, message, keyValues)
}

func (logger *Logger) log(level Level, message string, keyValues []string) {
	if level < logger.level {
		return
	}

	timestamp := time.Now().UTC()

	logger.mutex.Lock()
	defer logger.mutex.Unlock()

	if logger.format == FormatJSON {
		logger.writeJSON(timestamp, level, message, keyValues)
	} else {
		logger.writeHuman(timestamp, level, message, keyValues)
	}
}

func (logger *Logger) writeJSON(timestamp time.Time, level Level, message string, keyValues []string) {
	entry := jsonEntry{
		Timestamp: timestamp.Format(time.RFC3339Nano),
		Level:     level.String(),
		Component: logger.component,
		Message:   message,
	}

	if len(keyValues) >= 2 {
		entry.Fields = make(map[string]string, len(keyValues)/2)
		for fieldIndex := 0; fieldIndex+1 < len(keyValues); fieldIndex += 2 {
			entry.Fields[keyValues[fieldIndex]] = keyValues[fieldIndex+1]
		}
	}

	encoded, marshalError := json.Marshal(entry)
	if marshalError != nil {
		return
	}
	encoded = append(encoded, '\n')
	logger.output.Write(encoded)
}

func (logger *Logger) writeHuman(timestamp time.Time, level Level, message string, keyValues []string) {
	timeString := timestamp.Format("15:04:05.000")

	var line string
	if logger.component != "" {
		line = fmt.Sprintf("%s %-5s [%s] %s", timeString, level.String(), logger.component, message)
	} else {
		line = fmt.Sprintf("%s %-5s %s", timeString, level.String(), message)
	}

	for fieldIndex := 0; fieldIndex+1 < len(keyValues); fieldIndex += 2 {
		line += fmt.Sprintf(" %s=%s", keyValues[fieldIndex], keyValues[fieldIndex+1])
	}

	line += "\n"
	fmt.Fprint(logger.output, line)
}
