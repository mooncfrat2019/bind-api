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
	MaxQueueSize        = 100
	WorkerTimeout       = 30 * time.Second
)
