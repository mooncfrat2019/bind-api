package internal

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// NewTestDB создаёт тестовую БД
func NewTestDB() (*gorm.DB, error) {
	return gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
}
