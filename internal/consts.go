package internal

import "time"

const (
	JobCreateZone   JobType = "CREATE_ZONE"
	JobDeleteZone   JobType = "DELETE_ZONE"
	JobAddRecord    JobType = "ADD_RECORD"
	JobDeleteRecord JobType = "DELETE_RECORD"
	JobReload       JobType = "RELOAD"
)

// --- Константы ---
const (
	DefaultZoneDir      = "/var/named/"
	DefaultZoneConfFile = "/etc/named.zones.conf"
	DefaultNamedConf    = "/etc/named.conf"
	DefaultDbHost       = "localhost"
	DefaultDbPort       = "5432"
	DefaultDbUser       = "bindapi"
	DefaultDbName       = "bind_api"
	DefaultDbSSLMode    = "disable"
	DefaultDbSchema     = "public"
	DefaultTTL          = 3600
	DefaultRefresh      = 3600
	DefaultRetry        = 600
	DefaultExpire       = 604800
	DefaultNegative     = 3600

	// Настройки очереди (могут быть переопределены через .env)
	DefaultMaxQueueSize       = 1000
	DefaultWorkerTimeout      = 30 * time.Second
	DefaultBatchSize          = 50               // Размер пакета для batch-режима
	DefaultBatchInterval      = 5 * time.Second  // Интервал накопления пакета
	DefaultQueueThresholdLow  = 0.1              // 10% - переход в нормальный режим
	DefaultQueueThresholdHigh = 0.3              // 30% - переход в batch режим
	DefaultReloadInterval     = 10 * time.Second // Периодический reload в batch режиме
)

var (
	MaxQueueSize       int
	WorkerTimeout      time.Duration
	BatchSize          int
	BatchInterval      time.Duration
	QueueThresholdLow  float64 // 0.1 = 10%
	QueueThresholdHigh float64 // 0.3 = 30%
	ReloadInterval     time.Duration
)
