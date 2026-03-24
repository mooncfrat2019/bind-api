package internal

import (
	"encoding/json"
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

type CreateAPIKeyRequest struct {
	Name        string   `json:"name" binding:"required,min=3,max=100"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions" binding:"required,gt=0"`
	IPAddress   string   `json:"ip_address"`
	ExpiresIn   int      `json:"expires_in"`
}

type CreateAPIKeyResponse struct {
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	Permissions []string   `json:"permissions"`
	IPAddress   string     `json:"ip_address"`
	ExpiresAt   *time.Time `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
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

type APIKey struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Key         string `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
	Name        string `gorm:"type:varchar(100);not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`

	// ✅ ИСПРАВЛЕНО: используем jsonb вместо text[]
	Permissions string `gorm:"type:jsonb" json:"permissions"`

	IPAddress string     `gorm:"type:varchar(45)" json:"ip_address"`
	ExpiresAt *time.Time `gorm:"index" json:"expires_at"`

	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func (APIKey) TableName() string {
	if DbSchema != "" && DbSchema != "public" {
		return fmt.Sprintf("%s.api_keys", DbSchema)
	}
	return "api_keys"
}

// HasPermission проверяет наличие права
func (k *APIKey) HasPermission(perm string) bool {
	var perms []string
	if err := json.Unmarshal([]byte(k.Permissions), &perms); err != nil {
		return false
	}
	for _, p := range perms {
		if p == perm || p == "*" {
			return true
		}
	}
	return false
}

func (k *APIKey) IsExpired() bool {
	return k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt)
}

func (k *APIKey) BeforeCreate(tx *gorm.DB) error {
	if k.Key == "" {
		k.Key = generateSecureKey()
	}
	return nil
}
