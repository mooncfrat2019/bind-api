package internal

import (
	"database/sql"
	"os"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	// Импортируем modernc.org/sqlite драйвер
	_ "modernc.org/sqlite"
)

// NewTestDBModernC создаёт тестовую БД с modernc.org/sqlite
func NewTestDBModernC() (*gorm.DB, error) {
	// Создаём временный файл для БД
	tmpDir, err := os.MkdirTemp("", "testdb-*")
	if err != nil {
		return nil, err
	}

	dbPath := filepath.Join(tmpDir, "test.db")

	// Открываем соединение с modernc.org/sqlite
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Используем существующее соединение
	gormDB, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{})
	if err != nil {
		return nil, err
	}

	return gormDB, nil
}
