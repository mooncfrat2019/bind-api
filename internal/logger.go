package internal

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"gorm.io/gorm/logger"
)

// InitLogLevel инициализирует уровень логирования из переменной окружения
func InitLogLevel() {
	levelStr := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	switch levelStr {
	case "DEBUG":
		LogLevel = LevelDebug
	case "INFO", "":
		LogLevel = LevelInfo
	case "WARN", "WARNING":
		LogLevel = LevelWarn
	case "ERROR":
		LogLevel = LevelError
	default:
		LogLevel = LevelInfo
		log.Printf("Неизвестный уровень логирования '%s', используется INFO", levelStr)
	}
	log.Printf("Уровень логирования: %s", getLogLevelName())
}

func getLogLevelName() string {
	switch LogLevel {
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

// Debug логирует сообщения уровня DEBUG
func Debug(format string, v ...interface{}) {
	if LogLevel <= LevelDebug {
		log.Printf("[DEBUG] "+format, v...)
	}
}

// Debugf логирует сообщения уровня DEBUG с форматированием
func Debugf(format string, v ...interface{}) {
	if LogLevel <= LevelDebug {
		log.Printf("[DEBUG] "+format, v...)
	}
}

// Info логирует сообщения уровня INFO
func Info(format string, v ...interface{}) {
	if LogLevel <= LevelInfo {
		log.Printf("[INFO] "+format, v...)
	}
}

// Infof логирует сообщения уровня INFO с форматированием
func Infof(format string, v ...interface{}) {
	if LogLevel <= LevelInfo {
		log.Printf("[INFO] "+format, v...)
	}
}

// Warn логирует сообщения уровня WARN
func Warn(format string, v ...interface{}) {
	if LogLevel <= LevelWarn {
		log.Printf("[WARN] "+format, v...)
	}
}

// Warnf логирует сообщения уровня WARN с форматированием
func Warnf(format string, v ...interface{}) {
	if LogLevel <= LevelWarn {
		log.Printf("[WARN] "+format, v...)
	}
}

// Error логирует сообщения уровня ERROR
func Error(format string, v ...interface{}) {
	if LogLevel <= LevelError {
		log.Printf("[ERROR] "+format, v...)
	}
}

// Errorf логирует сообщения уровня ERROR с форматированием
func Errorf(format string, v ...interface{}) {
	if LogLevel <= LevelError {
		log.Printf("[ERROR] "+format, v...)
	}
}

// GORMLogger кастомный логгер для GORM
type GORMLogger struct {
	logger.Writer
	LogLevel logger.LogLevel
}

// NewGORMLogger создаёт новый логгер для GORM
func NewGORMLogger() *GORMLogger {
	var logLevel logger.LogLevel

	switch LogLevel {
	case LevelDebug:
		logLevel = logger.Info // GORM не имеет Debug уровня, используем Info
	case LevelInfo:
		logLevel = logger.Warn
	case LevelWarn:
		logLevel = logger.Error
	case LevelError:
		logLevel = logger.Silent
	default:
		logLevel = logger.Warn
	}

	return &GORMLogger{
		Writer:   log.New(os.Stdout, "\r\n", log.LstdFlags),
		LogLevel: logLevel,
	}
}

// LogMode возвращает логгер с указанным уровнем
func (l *GORMLogger) LogMode(level logger.LogLevel) logger.Interface {
	newlogger := *l
	newlogger.LogLevel = level
	return &newlogger
}

// Info реализует интерфейс logger.Interface
func (l *GORMLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Info {
		l.Printf("[GORM] "+msg, data...)
	}
}

// Warn реализует интерфейс logger.Interface
func (l *GORMLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Warn {
		l.Printf("[GORM] "+msg, data...)
	}
}

// Error реализует интерфейс logger.Interface
func (l *GORMLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Error {
		l.Printf("[GORM] "+msg, data...)
	}
}

// Trace реализует интерфейс logger.Interface
func (l *GORMLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	if l.LogLevel >= logger.Info {
		elapsed := time.Since(begin)
		sql, rows := fc()
		l.Printf("[GORM] [%v] %s (rows: %d)", elapsed, sql, rows)
	}
}
