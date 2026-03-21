package internal

import (
	"sync"

	"gorm.io/gorm"
)

var (
	// Конфигурация
	ZoneDir      string
	ZoneConfFile string
	NamedConf    string
	DbHost       string
	DbPort       string
	DbUser       string
	DbPassword   string
	DbName       string
	DbSSLMode    string
	DbSchema     string
	DbURL        string

	// Роль сервера
	AppRole string

	// База данных (только MASTER)
	Db *gorm.DB

	// Синхронизация
	SH *SyncHandler // Только MASTER
	RS *ReplicaSync // Только REPLICA

	// Очередь заданий (только MASTER)
	JQ      chan *Job
	JQMutex sync.Mutex

	// Блокировки файлов
	FileLocks      = make(map[string]*sync.Mutex)
	FileLocksMutex sync.Mutex
)
