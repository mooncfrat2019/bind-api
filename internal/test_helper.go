package internal

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var (
	globalTestMutex sync.Mutex
)

// SetupTestDB создаёт изолированную БД для каждого теста с блокировкой
func SetupTestDB(t *testing.T) *gorm.DB {
	globalTestMutex.Lock()
	defer globalTestMutex.Unlock()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"), &gorm.Config{
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err)

	return db
}

// SetGlobalDB безопасно устанавливает глобальную БД
func SetGlobalDB(db *gorm.DB) {
	globalTestMutex.Lock()
	defer globalTestMutex.Unlock()
	Db = db
}

// RestoreGlobalDB восстанавливает глобальную БД
func RestoreGlobalDB(oldDB *gorm.DB) {
	globalTestMutex.Lock()
	defer globalTestMutex.Unlock()
	Db = oldDB
}
