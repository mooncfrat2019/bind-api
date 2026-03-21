package internal

import (
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"

	"gorm.io/gorm"
)

type SyncHandler struct {
	db *gorm.DB
}

type Job struct {
	ID          int64
	Type        JobType
	ZoneName    string
	RecordName  string
	RecordType  string
	RecordValue string
	TTL         int
	ReversePtr  string
	Email       string
	ConfigFile  string
	NsIP        string
	ResponseCh  chan JobResult
	CreatedAt   time.Time
}

type JobResult struct {
	Success bool
	Message string
	Data    interface{}
	Error   error
}

// --- Структуры API ---

type Response struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type ZoneRequest struct {
	Name       string `json:"name" binding:"required"`
	Email      string `json:"email"`
	Type       string `json:"type"`
	NsIP       string `json:"ns_ip"`
	ConfigFile string `json:"config_file"`
}

type RecordRequest struct {
	Name       string `json:"name" binding:"required"`
	Type       string `json:"type" binding:"required"`
	Value      string `json:"value" binding:"required"`
	TTL        int    `json:"ttl"`
	ReversePtr string `json:"reverse_ptr"`
}

type ZoneInfo struct {
	Name        string `json:"name"`
	File        string `json:"file"`
	Type        string `json:"type"`
	ConfigFile  string `json:"config_file"`
	RecordCount int    `json:"record_count"`
}

type RecordInfo struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	TTL   int    `json:"ttl"`
	Value string `json:"value"`
}

type ZoneConfig struct {
	Name       string
	File       string
	Type       string
	ConfigFile string
}

type JobType string

// SyncStateData данные состояния для ответа реплике
type SyncStateData struct {
	Files     []SyncFileInfo `json:"files"`
	Timestamp time.Time      `json:"timestamp"`
}

// SyncFileResp обёртка для ответа файла
type SyncFileResp struct {
	Success bool             `json:"success"`
	Data    SyncFileResponse `json:"data"`
}

// ReplicaSyncStateResp ответ состояния для реплики
type ReplicaSyncStateResp struct {
	Success bool          `json:"success"`
	Data    SyncStateData `json:"data"`
}

type SyncFileInfo struct {
	FileType     string    `json:"file_type"`
	FileName     string    `json:"file_name"`
	ZoneName     string    `json:"zone_name"`
	Checksum     string    `json:"checksum"`
	Version      int       `json:"version"`
	LastModified time.Time `json:"last_modified"`
}

type SyncFileResponse struct {
	FileType     string    `json:"file_type"`
	FileName     string    `json:"file_name"`
	ZoneName     string    `json:"zone_name"`
	Checksum     string    `json:"checksum"`
	Version      int       `json:"version"`
	LastModified time.Time `json:"last_modified"`
	Content      string    `json:"content"`
}

// SyncState модель для состояния синхронизации
type SyncState struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	FileType     string    `gorm:"type:varchar(50);not null;index" json:"file_type"`
	FileName     string    `gorm:"type:varchar(500);not null;index" json:"file_name"`
	ZoneName     string    `gorm:"type:varchar(255);index" json:"zone_name"`
	Checksum     string    `gorm:"type:varchar(64);not null" json:"checksum"`
	Version      int       `gorm:"not null;index" json:"version"`
	Content      string    `gorm:"type:text" json:"content"`
	LastModified time.Time `gorm:"not null" json:"last_modified"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AuditLog модель для журнала аудита
type AuditLog struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	JobType     string     `gorm:"type:varchar(50);not null;index" json:"job_type"`
	ZoneName    string     `gorm:"type:varchar(255);index" json:"zone_name"`
	RecordName  string     `gorm:"type:varchar(255)" json:"record_name"`
	RecordType  string     `gorm:"type:varchar(20)" json:"record_type"`
	Status      string     `gorm:"type:varchar(20);not null;index" json:"status"`
	Error       string     `gorm:"type:text" json:"error"`
	CreatedAt   time.Time  `gorm:"index" json:"created_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type ReplicaSync struct {
	MasterURL         string
	APIToken          string
	Interval          time.Duration
	Enabled           bool
	Transform         ConfigTransform
	httpClient        *http.Client
	mu                sync.Mutex
	isSyncing         bool
	lastSyncTime      time.Time
	filesUpdatedCount int
}

type ConfigTransform struct {
	MasterIP            string
	ZoneType            string
	ZoneSubdir          string
	RemoveAllowTransfer bool
	AllowTransfer       string
	Replacements        []StringReplacement
}

type StringReplacement struct {
	Pattern     string
	Replacement string
	regex       *regexp.Regexp
}

func (SyncState) TableName() string {
	if DbSchema != "" && DbSchema != "public" {
		return fmt.Sprintf("%s.sync_states", DbSchema)
	}
	return "sync_states"
}

func (AuditLog) TableName() string {
	if DbSchema != "" && DbSchema != "public" {
		return fmt.Sprintf("%s.audit_logs", DbSchema)
	}
	return "audit_logs"
}
