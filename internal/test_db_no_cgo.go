//go:build !cgo

package internal

import (
	"errors"

	"gorm.io/gorm"
)

// NewTestDB для сборки без CGO возвращает ошибку
func NewTestDB() (*gorm.DB, error) {
	return nil, errors.New("CGO is required for tests with SQLite. Set CGO_ENABLED=1")
}
