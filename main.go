package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// --- Глобальные переменные ---
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
	appRole string

	// База данных (только MASTER)
	db *gorm.DB

	// Синхронизация
	syncHandler *SyncHandler // Только MASTER
	replicaSync *ReplicaSync // Только REPLICA

	// Очередь заданий (только MASTER)
	jobQueue      chan *Job
	jobQueueMutex sync.Mutex

	// Блокировки файлов
	fileLocks      = make(map[string]*sync.Mutex)
	fileLocksMutex sync.Mutex
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

// --- МОДЕЛИ GORM ---

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

func (AuditLog) TableName() string {
	if DbSchema != "" && DbSchema != "public" {
		return fmt.Sprintf("%s.audit_logs", DbSchema)
	}
	return "audit_logs"
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

func (SyncState) TableName() string {
	if DbSchema != "" && DbSchema != "public" {
		return fmt.Sprintf("%s.sync_states", DbSchema)
	}
	return "sync_states"
}

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

// --- Типы заданий ---

type JobType string

const (
	JobCreateZone   JobType = "CREATE_ZONE"
	JobDeleteZone   JobType = "DELETE_ZONE"
	JobAddRecord    JobType = "ADD_RECORD"
	JobDeleteRecord JobType = "DELETE_RECORD"
	JobReload       JobType = "RELOAD"
)

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

// --- Инициализация ---

func initConfig() {
	ZoneDir = os.Getenv("BIND_ZONE_DIR")
	if ZoneDir == "" {
		ZoneDir = DefaultZoneDir
	}

	ZoneConfFile = os.Getenv("BIND_ZONE_CONF")
	if ZoneConfFile == "" {
		ZoneConfFile = DefaultZoneConfFile
	}

	NamedConf = os.Getenv("BIND_NAMED_CONF")
	if NamedConf == "" {
		NamedConf = DefaultNamedConf
	}

	// PostgreSQL настройки
	DbURL = os.Getenv("BIND_API_DB_URL")

	if DbURL == "" {
		DbHost = os.Getenv("BIND_API_DB_HOST")
		if DbHost == "" {
			DbHost = DefaultDbHost
		}

		DbPort = os.Getenv("BIND_API_DB_PORT")
		if DbPort == "" {
			DbPort = DefaultDbPort
		}

		DbUser = os.Getenv("BIND_API_DB_USER")
		if DbUser == "" {
			DbUser = DefaultDbUser
		}

		DbPassword = os.Getenv("BIND_API_DB_PASSWORD")

		DbName = os.Getenv("BIND_API_DB_NAME")
		if DbName == "" {
			DbName = DefaultDbName
		}

		DbSSLMode = os.Getenv("BIND_API_DB_SSLMODE")
		if DbSSLMode == "" {
			DbSSLMode = DefaultDbSSLMode
		}

		DbSchema = os.Getenv("BIND_API_DB_SCHEMA")
		if DbSchema == "" {
			DbSchema = DefaultDbSchema
		}
	}

	if !strings.HasSuffix(ZoneDir, "/") {
		ZoneDir += "/"
	}

	log.Printf("Конфигурация: ZoneDir=%s, ZoneConfFile=%s, NamedConf=%s", ZoneDir, ZoneConfFile, NamedConf)
	if DbURL != "" || DbHost != "" {
		log.Printf("PostgreSQL: Host=%s, Port=%s, User=%s, DB=%s, Schema=%s", DbHost, DbPort, DbUser, DbName, DbSchema)
	}
}

// --- Инициализация БД (только MASTER) ---

func initDatabase() error {
	// На REPLICA пропускаем инициализацию БД
	if appRole == "replica" {
		log.Println("Роль REPLICA - база данных не инициализируется")
		return nil
	}

	var err error
	var dsn string

	if DbURL != "" {
		dsn = DbURL
	} else {
		dsn = fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
			DbHost, DbPort, DbUser, DbPassword, DbName, DbSSLMode,
		)
	}

	log.Printf("Подключение к PostgreSQL...")

	maxRetries := 5
	for i := 0; i < maxRetries; i++ {
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Info),
		})
		if err != nil {
			log.Printf("Ошибка подключения (попытка %d/%d): %v", i+1, maxRetries, err)
			time.Sleep(2 * time.Second)
			continue
		}

		sqlDB, err := db.DB()
		if err != nil {
			log.Printf("Ошибка получения SQL DB: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if err = sqlDB.Ping(); err != nil {
			log.Printf("Ошибка ping (попытка %d/%d): %v", i+1, maxRetries, err)
			time.Sleep(2 * time.Second)
			continue
		}

		sqlDB.SetMaxOpenConns(25)
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(5 * time.Minute)

		log.Println("Успешное подключение к PostgreSQL")
		break
	}

	if err != nil {
		return fmt.Errorf("не удалось подключиться к PostgreSQL после %d попыток: %v", maxRetries, err)
	}

	// НЕ устанавливаем search_path - схема указывается в TableName()

	log.Println("Выполнение миграций базы данных...")
	if err := db.AutoMigrate(&AuditLog{}, &SyncState{}); err != nil {
		return fmt.Errorf("ошибка миграции: %v", err)
	}

	log.Println("База данных PostgreSQL инициализирована")
	return nil
}

// --- Очередь заданий (только MASTER) ---

func initJobQueue() {
	jobQueue = make(chan *Job, MaxQueueSize)
	go jobWorker()
	log.Println("Очередь заданий инициализирована")
}

func jobWorker() {
	for job := range jobQueue {
		processJob(job)
	}
}

func processJob(job *Job) {
	log.Printf("Обработка задания %d: %s для зоны %s", job.ID, job.Type, job.ZoneName)

	logAudit(job, "STARTED", "")

	var result JobResult

	switch job.Type {
	case JobCreateZone:
		result = executeCreateZone(job)
	case JobDeleteZone:
		result = executeDeleteZone(job)
	case JobAddRecord:
		result = executeAddRecord(job)
	case JobDeleteRecord:
		result = executeDeleteRecord(job)
	case JobReload:
		result = executeReload()
	default:
		result = JobResult{Success: false, Error: fmt.Errorf("неизвестный тип задания")}
	}

	if result.Success {
		logAudit(job, "COMPLETED", "")
	} else {
		logAudit(job, "FAILED", result.Error.Error())
	}

	job.ResponseCh <- result
	close(job.ResponseCh)
}

func submitJob(job *Job) (*JobResult, error) {
	jobQueueMutex.Lock()
	if len(jobQueue) >= MaxQueueSize {
		jobQueueMutex.Unlock()
		return nil, fmt.Errorf("очередь переполнена")
	}
	jobQueueMutex.Unlock()

	job.ResponseCh = make(chan JobResult, 1)
	job.CreatedAt = time.Now()

	var lastID uint
	db.Model(&AuditLog{}).Select("COALESCE(MAX(id), 0)").Scan(&lastID)
	job.ID = int64(lastID) + 1

	jobQueue <- job

	select {
	case result := <-job.ResponseCh:
		return &result, nil
	case <-time.After(WorkerTimeout):
		return &JobResult{
			Success: false,
			Error:   fmt.Errorf("таймаут выполнения задания"),
		}, nil
	}
}

// --- Аудит (только MASTER) ---

func logAudit(job *Job, status string, errMsg string) {
	audit := AuditLog{
		JobType:    string(job.Type),
		ZoneName:   job.ZoneName,
		RecordName: job.RecordName,
		RecordType: job.RecordType,
		Status:     status,
		Error:      errMsg,
		CreatedAt:  job.CreatedAt,
	}

	if status != "STARTED" {
		now := time.Now()
		audit.CompletedAt = &now
	}

	if err := db.Create(&audit).Error; err != nil {
		log.Printf("WARNING: Не удалось записать аудит: %v", err)
	}
}

// --- Блокировки файлов ---

func getFileLock(filePath string) *sync.Mutex {
	fileLocksMutex.Lock()
	defer fileLocksMutex.Unlock()

	if _, exists := fileLocks[filePath]; !exists {
		fileLocks[filePath] = &sync.Mutex{}
	}
	return fileLocks[filePath]
}

func withFileLock(filePath string, fn func() error) error {
	lock := getFileLock(filePath)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

// --- Утилиты ---

func sendResponse(c *gin.Context, status int, success bool, message string, data interface{}) {
	c.JSON(status, Response{
		Success: success,
		Message: message,
		Data:    data,
	})
}

func reloadBind() error {
	log.Println("Выполнение rndc reload...")
	cmd := exec.Command("rndc", "reload")
	out, err := cmd.CombinedOutput()
	log.Printf("rndc reload output: %s, error: %v", string(out), err)
	if err != nil {
		return fmt.Errorf("rndc reload failed: %v, output: %s", err, string(out))
	}
	log.Println("rndc reload выполнен успешно")
	return nil
}

func fixPermissions(filename string) error {
	cmd := exec.Command("chown", "named:named", filename)
	if err := cmd.Run(); err != nil {
		log.Printf("WARNING: chown failed for %s: %v", filename, err)
	}
	cmd = exec.Command("chmod", "644", filename)
	if err := cmd.Run(); err != nil {
		log.Printf("WARNING: chmod failed for %s: %v", filename, err)
	}
	cmd = exec.Command("restorecon", "-v", filename)
	_ = cmd.Run()
	return nil
}

func validateZoneName(name string) bool {
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, ";") {
		return false
	}
	return len(name) > 0 && len(name) < 255
}

func validateRecordName(name string) bool {
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, ";") {
		return false
	}
	return true
}

func incrementSerial(zoneFile string) error {
	log.Printf("Увеличение Serial в файле: %s", zoneFile)

	if _, err := os.Stat(zoneFile); os.IsNotExist(err) {
		return fmt.Errorf("файл зоны не существует: %s", zoneFile)
	}

	content, err := os.ReadFile(zoneFile)
	if err != nil {
		return fmt.Errorf("не удалось прочитать файл: %v", err)
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string
	inSoa := false
	soaComplete := false
	serialUpdated := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.Contains(strings.ToUpper(trimmed), "SOA") && !soaComplete {
			inSoa = true
			newLines = append(newLines, line)
			continue
		}

		if inSoa && !soaComplete {
			if strings.Contains(line, ")") {
				soaComplete = true
			}

			fields := strings.Fields(line)
			updated := false
			for i, field := range fields {
				cleanField := strings.Trim(field, "()_;")
				if num, err := strconv.ParseUint(cleanField, 10, 32); err == nil && num >= 2020010100 {
					newNum := num + 1
					fields[i] = strings.Replace(field, cleanField, strconv.FormatUint(newNum, 10), 1)
					newLines = append(newLines, strings.Join(fields, " "))
					updated = true
					serialUpdated = true
					log.Printf("Serial увеличен: %d -> %d", num, newNum)
					break
				}
			}
			if !updated {
				newLines = append(newLines, line)
			}
			continue
		}

		newLines = append(newLines, line)
	}

	if !serialUpdated {
		log.Println("WARNING: Serial не был увеличен (не найден в файле)")
	}

	return os.WriteFile(zoneFile, []byte(strings.Join(newLines, "\n")), 0644)
}

func readZoneFileSimple(zoneFile string) ([]RecordInfo, error) {
	file, err := os.Open(zoneFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var records []RecordInfo
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "$") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}

		rec := RecordInfo{}
		idx := 0
		rec.Name = parts[idx]
		idx++

		for idx < len(parts) {
			if _, err := fmt.Sscanf(parts[idx], "%d", new(int)); err == nil {
				fmt.Sscanf(parts[idx], "%d", &rec.TTL)
				idx++
				continue
			}
			if parts[idx] == "IN" || parts[idx] == "CH" || parts[idx] == "HS" {
				idx++
				continue
			}
			break
		}

		if idx < len(parts) {
			rec.Type = strings.ToUpper(parts[idx])
			idx++
		} else {
			continue
		}

		if idx < len(parts) {
			rec.Value = strings.Join(parts[idx:], " ")
		}

		if rec.Type == "A" || rec.Type == "AAAA" || rec.Type == "CNAME" || rec.Type == "MX" || rec.Type == "NS" || rec.Type == "TXT" || rec.Type == "SOA" || rec.Type == "PTR" {
			records = append(records, rec)
		}
	}

	return records, scanner.Err()
}

func appendRecordToFile(zoneFile, recordLine string) error {
	log.Printf("Добавление записи в файл %s: %s", zoneFile, recordLine)
	f, err := os.OpenFile(zoneFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", recordLine)
	return err
}

func deleteRecordFromFile(zoneFile, recordName, recordType string) error {
	file, err := os.Open(zoneFile)
	if err != nil {
		return err
	}

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "$") {
			lines = append(lines, line)
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) >= 2 {
			name := fields[0]
			foundType := false
			for _, f := range fields {
				if strings.ToUpper(f) == strings.ToUpper(recordType) {
					foundType = true
					break
				}
			}

			fqdnName := recordName
			if !strings.HasSuffix(fqdnName, ".") && fqdnName != "@" {
				fqdnName += "."
			}
			if (name == recordName || name == fqdnName || (recordName == "@" && name == "")) && foundType {
				log.Printf("Удаление записи: %s %s", name, recordType)
				continue
			}
		}
		lines = append(lines, line)
	}
	file.Close()

	if err := scanner.Err(); err != nil {
		return err
	}

	return os.WriteFile(zoneFile, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

// --- Парсинг конфига ---

func parseZoneConfig() ([]ZoneConfig, error) {
	var zones []ZoneConfig
	configFiles := []string{}

	if _, err := os.Stat(NamedConf); err == nil {
		configFiles = append(configFiles, NamedConf)
	}
	if _, err := os.Stat(ZoneConfFile); err == nil && ZoneConfFile != NamedConf {
		configFiles = append(configFiles, ZoneConfFile)
	}

	log.Printf("Парсинг конфигов: %v", configFiles)

	zoneRegex := regexp.MustCompile(`zone\s+"([^"]+)"\s+(?:IN\s+)?\{[^}]*file\s+"([^"]+)"`)

	for _, configFile := range configFiles {
		content, err := os.ReadFile(configFile)
		if err != nil {
			log.Printf("WARNING: Не удалось прочитать конфиг %s: %v", configFile, err)
			continue
		}

		matches := zoneRegex.FindAllStringSubmatch(string(content), -1)
		for _, match := range matches {
			if len(match) >= 3 {
				zoneName := match[1]
				zoneFile := match[2]

				if !filepath.IsAbs(zoneFile) {
					possiblePaths := []string{
						filepath.Join(ZoneDir, zoneFile),
						filepath.Join(filepath.Dir(configFile), zoneFile),
						zoneFile,
					}

					found := false
					for _, path := range possiblePaths {
						if _, err := os.Stat(path); err == nil {
							zoneFile = path
							found = true
							break
						}
					}

					if !found {
						zoneFile = possiblePaths[0]
					}
				}

				zoneType := "forward"
				if strings.Contains(zoneName, "in-addr.arpa") || strings.Contains(zoneName, "ip6.arpa") {
					zoneType = "reverse"
				}

				zones = append(zones, ZoneConfig{
					Name:       zoneName,
					File:       zoneFile,
					Type:       zoneType,
					ConfigFile: configFile,
				})
				log.Printf("Найдена зона: %s -> %s (конфиг: %s)", zoneName, zoneFile, configFile)
			}
		}
	}

	return zones, nil
}

func getZoneFromConfig(zoneName string) (*ZoneConfig, bool) {
	zones, err := parseZoneConfig()
	if err != nil {
		log.Printf("Ошибка парсинга конфига: %v", err)
		return nil, false
	}

	for _, zone := range zones {
		if zone.Name == zoneName {
			log.Printf("Найдена зона %s: файл=%s, конфиг=%s", zoneName, zone.File, zone.ConfigFile)
			return &zone, true
		}
	}

	log.Printf("Зона %s не найдена в конфиге", zoneName)
	return nil, false
}

func zoneExistsInConfig(zoneName string) bool {
	_, exists := getZoneFromConfig(zoneName)
	return exists
}

// --- Reverse DNS ---

func getReverseZoneName(ip string) (string, error) {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return "", fmt.Errorf("неверный IP адрес")
	}

	if parsedIP.To4() != nil {
		parts := strings.Split(parsedIP.To4().String(), ".")
		if len(parts) != 4 {
			return "", fmt.Errorf("неверный формат IPv4")
		}
		return fmt.Sprintf("%s.%s.%s.in-addr.arpa", parts[2], parts[1], parts[0]), nil
	}

	return "", fmt.Errorf("IPv6 обратные зоны требуют ручной настройки")
}

func getPtrRecordName(ip string) (string, error) {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return "", fmt.Errorf("неверный IP адрес")
	}

	if parsedIP.To4() != nil {
		parts := strings.Split(parsedIP.To4().String(), ".")
		if len(parts) != 4 {
			return "", fmt.Errorf("неверный формат IPv4")
		}
		return parts[3], nil
	}

	return "", fmt.Errorf("IPv6 требует ручной настройки")
}

func addPtrRecord(ip string, ptrName string, ttl int) error {
	log.Printf("Добавление PTR записи: IP=%s, PTR=%s", ip, ptrName)

	reverseZoneName, err := getReverseZoneName(ip)
	if err != nil {
		return fmt.Errorf("ошибка получения имени обратной зоны: %v", err)
	}
	log.Printf("Имя обратной зоны: %s", reverseZoneName)

	zone, exists := getZoneFromConfig(reverseZoneName)
	if !exists {
		return fmt.Errorf("обратная зона %s не найдена в конфигурации", reverseZoneName)
	}

	log.Printf("Файл обратной зоны: %s, конфиг: %s", zone.File, zone.ConfigFile)

	if _, err := os.Stat(zone.File); os.IsNotExist(err) {
		return fmt.Errorf("файл обратной зоны не существует: %s", zone.File)
	}

	ptrRecordName, err := getPtrRecordName(ip)
	if err != nil {
		return fmt.Errorf("ошибка получения имени PTR записи: %v", err)
	}
	log.Printf("Имя PTR записи (октет): %s", ptrRecordName)

	if ttl == 0 {
		ttl = DefaultTTL
	}

	recordLine := fmt.Sprintf("%s\t%d\tIN\tPTR\t%s", ptrRecordName, ttl, ptrName)
	log.Printf("Строка PTR записи: %s", recordLine)

	if err := appendRecordToFile(zone.File, recordLine); err != nil {
		return fmt.Errorf("ошибка добавления записи в файл: %v", err)
	}

	if err := incrementSerial(zone.File); err != nil {
		return fmt.Errorf("ошибка обновления Serial: %v", err)
	}

	if err := fixPermissions(zone.File); err != nil {
		return err
	}

	log.Printf("PTR запись добавлена успешно")
	return nil
}

func deletePtrRecord(ip string) error {
	log.Printf("Удаление PTR записи для IP: %s", ip)

	reverseZoneName, err := getReverseZoneName(ip)
	if err != nil {
		return err
	}

	zone, exists := getZoneFromConfig(reverseZoneName)
	if !exists {
		log.Printf("Обратная зона %s не найдена в конфиге, пропускаем удаление PTR", reverseZoneName)
		return nil
	}

	if _, err := os.Stat(zone.File); os.IsNotExist(err) {
		log.Printf("Файл обратной зоны не существует: %s", zone.File)
		return nil
	}

	ptrRecordName, err := getPtrRecordName(ip)
	if err != nil {
		return err
	}

	if err := deleteRecordFromFile(zone.File, ptrRecordName, "PTR"); err != nil {
		return err
	}

	if err := incrementSerial(zone.File); err != nil {
		return err
	}

	if err := fixPermissions(zone.File); err != nil {
		return err
	}

	log.Printf("PTR запись удалена успешно")
	return nil
}

// --- Удаление зоны из конфига ---

func removeZoneFromConfig(configFile, zoneName string) error {
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		log.Printf("Файл конфига не существует: %s", configFile)
		return nil
	}

	origInfo, err := os.Stat(configFile)
	if err != nil {
		return fmt.Errorf("ошибка получения информации о файле: %v", err)
	}
	origMode := origInfo.Mode()
	log.Printf("Оригинальные права файла %s: %s", configFile, origMode)

	content, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("ошибка чтения конфига %s: %v", configFile, err)
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string
	skip := false
	braceCount := 0
	zoneFound := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !skip && strings.HasPrefix(trimmed, "zone") && strings.Contains(trimmed, fmt.Sprintf(`"%s"`, zoneName)) {
			log.Printf("Найдена зона %s на строке %d", zoneName, i+1)
			zoneFound = true
			skip = true
			braceCount += strings.Count(line, "{")
			braceCount -= strings.Count(line, "}")
			continue
		}

		if skip {
			braceCount += strings.Count(line, "{")
			braceCount -= strings.Count(line, "}")

			if braceCount <= 0 {
				log.Printf("Конец блока зоны на строке %d", i+1)
				skip = false
				braceCount = 0
			}
			continue
		}

		newLines = append(newLines, line)
	}

	if !zoneFound {
		log.Printf("Зона %s не найдена в конфиге %s", zoneName, configFile)
		return nil
	}

	tmpFile := configFile + ".tmp"
	newContent := strings.Join(newLines, "\n")
	newContent = strings.TrimSpace(newContent) + "\n"

	if err := os.WriteFile(tmpFile, []byte(newContent), origMode); err != nil {
		return fmt.Errorf("ошибка записи временного файла: %v", err)
	}

	cmd := exec.Command("chown", "--reference="+configFile, tmpFile)
	if err := cmd.Run(); err != nil {
		cmd = exec.Command("chown", "root:named", tmpFile)
		_ = cmd.Run()
	}

	cmd = exec.Command("named-checkconf", tmpFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("синтаксическая ошибка в конфиге: %s", string(out))
	}

	if err := os.Rename(tmpFile, configFile); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("ошибка замены конфига: %v", err)
	}

	cmd = exec.Command("chmod", fmt.Sprintf("%o", origMode), configFile)
	_ = cmd.Run()

	cmd = exec.Command("chown", "root:named", configFile)
	_ = cmd.Run()

	log.Printf("Зона %s удалена из конфига %s", zoneName, configFile)
	return nil
}

// --- Выполнители заданий (только MASTER) ---

func getServerIPs() []string {
	var ips []string

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		log.Printf("WARNING: Не удалось получить IP адреса: %v", err)
		return []string{"127.0.0.1"}
	}

	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				ips = append(ips, ipNet.IP.String())
			}
		}
	}

	if len(ips) == 0 {
		return []string{"127.0.0.1"}
	}

	return ips
}

func executeCreateZone(job *Job) JobResult {
	if !validateZoneName(job.ZoneName) {
		return JobResult{Success: false, Error: fmt.Errorf("недопустимое имя зоны")}
	}

	email := job.Email
	if email == "" {
		email = "admin." + job.ZoneName
	}

	zoneType := "forward"
	if job.Type == "reverse" || strings.Contains(job.ZoneName, "in-addr.arpa") {
		zoneType = "reverse"
	}

	if zoneExistsInConfig(job.ZoneName) {
		return JobResult{Success: false, Error: fmt.Errorf("зона уже существует")}
	}

	targetConfigFile := job.ConfigFile
	if targetConfigFile == "" {
		zones, _ := parseZoneConfig()
		if len(zones) > 0 {
			targetConfigFile = zones[0].ConfigFile
		} else {
			targetConfigFile = NamedConf
		}
	}

	var zoneFile string
	if zoneType == "reverse" {
		zoneFile = filepath.Join(ZoneDir, job.ZoneName+".rev")
	} else {
		zoneFile = filepath.Join(ZoneDir, job.ZoneName+".zone")
	}

	if _, err := os.Stat(zoneFile); err == nil {
		return JobResult{Success: false, Error: fmt.Errorf("файл зоны уже существует")}
	}

	now := time.Now()
	serial := fmt.Sprintf("%d%02d%02d01", now.Year(), now.Month(), now.Day())

	soaEmail := strings.Replace(email, "@", ".", -1)
	if !strings.HasSuffix(soaEmail, ".") {
		soaEmail += "."
	}

	nsIP := job.NsIP
	if nsIP == "" {
		serverIPs := getServerIPs()
		if len(serverIPs) > 0 {
			nsIP = serverIPs[0]
		} else {
			nsIP = "127.0.0.1"
		}
	}

	zoneContent := fmt.Sprintf(`$TTL %d
@	IN	SOA	ns1.%s. %s (
					%s	; Serial
					%d	; Refresh
					%d	; Retry
					%d	; Expire
					%d )	; Negative Cache TTL
;
@	IN	NS	ns1.%s.
ns1	%d	IN	A	%s
@	%d	IN	A	%s
`, DefaultTTL, job.ZoneName, soaEmail, serial, DefaultRefresh, DefaultRetry, DefaultExpire, DefaultNegative, job.ZoneName, DefaultTTL, nsIP, DefaultTTL, nsIP)

	err := withFileLock(zoneFile, func() error {
		return os.WriteFile(zoneFile, []byte(zoneContent), 0644)
	})
	if err != nil {
		return JobResult{Success: false, Error: fmt.Errorf("не удалось создать файл зоны: %v", err)}
	}

	if err := fixPermissions(zoneFile); err != nil {
		return JobResult{Success: false, Error: fmt.Errorf("ошибка прав доступа: %v", err)}
	}

	zoneConfig := fmt.Sprintf(`
zone "%s" IN {
         type master;
         file "%s";
         allow-update { none; };
};
`, job.ZoneName, filepath.Base(zoneFile))

	err = withFileLock(targetConfigFile, func() error {
		confFile, err := os.OpenFile(targetConfigFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
		if err != nil {
			return err
		}
		defer confFile.Close()
		_, err = confFile.WriteString(zoneConfig)
		return err
	})
	if err != nil {
		return JobResult{Success: false, Error: fmt.Errorf("ошибка записи в конфиг: %v", err)}
	}

	cmd := exec.Command("chown", "root:named", targetConfigFile)
	_ = cmd.Run()
	cmd = exec.Command("chmod", "640", targetConfigFile)
	_ = cmd.Run()

	cmd = exec.Command("named-checkconf")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return JobResult{Success: false, Error: fmt.Errorf("ошибка в конфигурации: %s", string(out))}
	}

	cmd = exec.Command("named-checkzone", job.ZoneName, zoneFile)
	out, err = cmd.CombinedOutput()
	if err != nil {
		return JobResult{Success: false, Error: fmt.Errorf("ошибка в файле зоны: %s", string(out))}
	}

	if err := reloadBind(); err != nil {
		return JobResult{Success: false, Error: err}
	}

	// Обновляем состояние для синхронизации
	if syncHandler != nil {
		syncHandler.UpdateSyncState("named_conf", NamedConf, "", NamedConf, "api")
		syncHandler.UpdateSyncState("zone_conf", ZoneConfFile, "", ZoneConfFile, "api")
		syncHandler.UpdateSyncState("zone_file", zoneFile, job.ZoneName, zoneFile, "api")
	}

	return JobResult{
		Success: true,
		Message: fmt.Sprintf("Зона %s (%s) создана в %s", job.ZoneName, zoneType, targetConfigFile),
		Data: gin.H{
			"zone_file":   zoneFile,
			"config_file": targetConfigFile,
			"ns1_ip":      nsIP,
		},
	}
}

func executeDeleteZone(job *Job) JobResult {
	if !validateZoneName(job.ZoneName) {
		return JobResult{Success: false, Error: fmt.Errorf("недопустимое имя зоны")}
	}

	zone, exists := getZoneFromConfig(job.ZoneName)
	if !exists {
		return JobResult{Success: false, Error: fmt.Errorf("зона не найдена в конфигурации")}
	}

	log.Printf("Удаление зоны %s: файл=%s, конфиг=%s", job.ZoneName, zone.File, zone.ConfigFile)

	err := withFileLock(zone.File, func() error {
		if _, err := os.Stat(zone.File); err == nil {
			return os.Remove(zone.File)
		}
		return nil
	})
	if err != nil {
		return JobResult{Success: false, Error: fmt.Errorf("не удалось удалить файл зоны: %v", err)}
	}

	err = withFileLock(zone.ConfigFile, func() error {
		return removeZoneFromConfig(zone.ConfigFile, job.ZoneName)
	})
	if err != nil {
		return JobResult{Success: false, Error: err}
	}

	cmd := exec.Command("named-checkconf")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return JobResult{Success: false, Error: fmt.Errorf("ошибка в конфигурации: %s", string(out))}
	}

	if err := reloadBind(); err != nil {
		return JobResult{Success: false, Error: err}
	}

	// Обновляем состояние для синхронизации
	if syncHandler != nil {
		syncHandler.UpdateSyncState("named_conf", NamedConf, "", NamedConf, "api")
		syncHandler.UpdateSyncState("zone_conf", ZoneConfFile, "", ZoneConfFile, "api")
	}

	return JobResult{
		Success: true,
		Message: fmt.Sprintf("Зона %s удалена из %s", job.ZoneName, zone.ConfigFile),
		Data: gin.H{
			"config_file": zone.ConfigFile,
			"zone_file":   zone.File,
		},
	}
}

func executeAddRecord(job *Job) JobResult {
	if !validateZoneName(job.ZoneName) {
		return JobResult{Success: false, Error: fmt.Errorf("недопустимое имя зоны")}
	}

	if !validateRecordName(job.RecordName) {
		return JobResult{Success: false, Error: fmt.Errorf("недопустимое имя записи")}
	}

	recordType := strings.ToUpper(job.RecordType)
	if recordType != "A" && recordType != "AAAA" && recordType != "CNAME" && recordType != "MX" && recordType != "TXT" && recordType != "NS" {
		return JobResult{Success: false, Error: fmt.Errorf("поддерживаются только A, AAAA, CNAME, MX, TXT, NS")}
	}

	if recordType == "A" {
		ip := net.ParseIP(job.RecordValue)
		if ip == nil || ip.To4() == nil {
			return JobResult{Success: false, Error: fmt.Errorf("неверный IPv4 адрес")}
		}
	}
	if recordType == "AAAA" {
		ip := net.ParseIP(job.RecordValue)
		if ip == nil || ip.To4() != nil {
			return JobResult{Success: false, Error: fmt.Errorf("неверный IPv6 адрес")}
		}
	}

	zone, exists := getZoneFromConfig(job.ZoneName)
	if !exists {
		return JobResult{Success: false, Error: fmt.Errorf("зона не найдена в конфигурации")}
	}

	ttl := job.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}

	recordLine := fmt.Sprintf("%s\t%d\tIN\t%s\t%s", job.RecordName, ttl, recordType, job.RecordValue)

	err := withFileLock(zone.File, func() error {
		if err := appendRecordToFile(zone.File, recordLine); err != nil {
			return err
		}
		return incrementSerial(zone.File)
	})
	if err != nil {
		return JobResult{Success: false, Error: fmt.Errorf("ошибка записи в файл зоны: %v", err)}
	}

	if (recordType == "A" || recordType == "AAAA") && job.ReversePtr != "" {
		ptrName := job.ReversePtr
		if !strings.HasSuffix(ptrName, ".") {
			ptrName += "."
		}
		if err := addPtrRecord(job.RecordValue, ptrName, ttl); err != nil {
			log.Printf("WARNING: Не удалось создать PTR запись: %v", err)
			return JobResult{
				Success: true,
				Message: "Запись добавлена (PTR не создана: " + err.Error() + ")",
			}
		}
	}

	if err := fixPermissions(zone.File); err != nil {
		return JobResult{Success: false, Error: fmt.Errorf("ошибка прав: %v", err)}
	}

	if err := reloadBind(); err != nil {
		return JobResult{Success: false, Error: err}
	}

	// Обновляем состояние для синхронизации
	if syncHandler != nil {
		syncHandler.UpdateSyncState("zone_file", zone.File, job.ZoneName, zone.File, "api")
	}

	return JobResult{Success: true, Message: "Запись добавлена"}
}

func executeDeleteRecord(job *Job) JobResult {
	if !validateZoneName(job.ZoneName) {
		return JobResult{Success: false, Error: fmt.Errorf("недопустимое имя зоны")}
	}

	if !validateRecordName(job.RecordName) {
		return JobResult{Success: false, Error: fmt.Errorf("недопустимое имя записи")}
	}

	zone, exists := getZoneFromConfig(job.ZoneName)
	if !exists {
		return JobResult{Success: false, Error: fmt.Errorf("зона не найдена в конфигурации")}
	}

	recordType := strings.ToUpper(job.RecordType)

	if recordType == "A" || recordType == "AAAA" {
		records, err := readZoneFileSimple(zone.File)
		if err == nil {
			for _, rec := range records {
				if rec.Name == job.RecordName && rec.Type == recordType {
					if err := deletePtrRecord(rec.Value); err != nil {
						log.Printf("WARNING: Не удалось удалить PTR запись: %v", err)
					}
					break
				}
			}
		}
	}

	err := withFileLock(zone.File, func() error {
		if err := deleteRecordFromFile(zone.File, job.RecordName, recordType); err != nil {
			return err
		}
		return incrementSerial(zone.File)
	})
	if err != nil {
		return JobResult{Success: false, Error: fmt.Errorf("ошибка удаления записи: %v", err)}
	}

	if err := fixPermissions(zone.File); err != nil {
		return JobResult{Success: false, Error: fmt.Errorf("ошибка прав: %v", err)}
	}

	if err := reloadBind(); err != nil {
		return JobResult{Success: false, Error: err}
	}

	// Обновляем состояние для синхронизации
	if syncHandler != nil {
		syncHandler.UpdateSyncState("zone_file", zone.File, job.ZoneName, zone.File, "api")
	}

	return JobResult{Success: true, Message: "Запись удалена"}
}

func executeReload() JobResult {
	if err := reloadBind(); err != nil {
		return JobResult{Success: false, Error: err}
	}
	return JobResult{Success: true, Message: "BIND перезагружен"}
}

// --- Sync Handler (только MASTER) ---

type SyncHandler struct {
	db *gorm.DB
}

func NewSyncHandler(db *gorm.DB) *SyncHandler {
	return &SyncHandler{db: db}
}

func (h *SyncHandler) syncAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("X-Sync-Token")
		expectedToken := os.Getenv("SYNC_API_TOKEN")

		if token == "" || token != expectedToken {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Неверный токен синхронизации",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// GetSyncFileByQuery получает файл по типу и имени через query-параметры
func (h *SyncHandler) GetSyncFileByQuery(c *gin.Context) {
	fileType := c.Query("type")
	fileName := c.Query("name")

	if fileType == "" || fileName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Отсутствуют параметры type или name",
		})
		return
	}

	log.Printf("Запрос файла (query): type=%s, name=%s", fileType, fileName)

	var state SyncState
	// Ищем точно по имени файла
	if err := h.db.Where("file_type = ? AND file_name = ?", fileType, fileName).
		First(&state).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": "Файл не найден",
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Файл получен",
		"data": SyncFileResponse{
			FileType:     state.FileType,
			FileName:     state.FileName,
			ZoneName:     state.ZoneName,
			Checksum:     state.Checksum,
			Version:      state.Version,
			LastModified: state.LastModified,
			Content:      state.Content,
		},
	})
}

func (h *SyncHandler) GetSyncState(c *gin.Context) {
	var states []SyncState
	if err := h.db.Find(&states).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Ошибка получения состояния",
			"error":   err.Error(),
		})
		return
	}

	files := make([]SyncFileInfo, 0, len(states))
	for _, state := range states {
		files = append(files, SyncFileInfo{
			FileType:     state.FileType,
			FileName:     state.FileName,
			ZoneName:     state.ZoneName,
			Checksum:     state.Checksum,
			Version:      state.Version,
			LastModified: state.LastModified,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Состояние синхронизации",
		"data": gin.H{
			"files":       files,
			"timestamp":   time.Now(),
			"master_host": os.Getenv("MASTER_URL"),
		},
	})
}

func (h *SyncHandler) GetSyncFile(c *gin.Context) {
	fileType := c.Param("fileType")
	fileName := c.Param("fileName")

	// URL-декодируем fileName
	decodedFileName, err := url.QueryUnescape(fileName)
	if err != nil {
		decodedFileName = fileName
	}

	log.Printf("Запрос файла: type=%s, name=%s (decoded: %s)", fileType, fileName, decodedFileName)

	var state SyncState
	if err := h.db.Where("file_type = ? AND file_name = ?", fileType, decodedFileName).
		First(&state).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": "Файл не найден",
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Файл получен",
		"data": SyncFileResponse{
			FileType:     state.FileType,
			FileName:     state.FileName,
			ZoneName:     state.ZoneName,
			Checksum:     state.Checksum,
			Version:      state.Version,
			LastModified: state.LastModified,
			Content:      state.Content,
		},
	})
}

func (h *SyncHandler) GetSyncZones(c *gin.Context) {
	var states []SyncState
	if err := h.db.Where("file_type = ?", "zone_file").Find(&states).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Ошибка получения зон",
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Список зон",
		"data": gin.H{
			"zones":     states,
			"timestamp": time.Now(),
		},
	})
}

func (h *SyncHandler) GetSyncZone(c *gin.Context) {
	zoneName := c.Param("zoneName")

	var state SyncState
	if err := h.db.Where("file_type = ? AND zone_name = ?", "zone_file", zoneName).
		First(&state).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": "Зона не найдена",
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Зона получена",
		"data": SyncFileResponse{
			FileType:     state.FileType,
			FileName:     state.FileName,
			ZoneName:     state.ZoneName,
			Checksum:     state.Checksum,
			Version:      state.Version,
			LastModified: state.LastModified,
			Content:      state.Content,
		},
	})
}

func (h *SyncHandler) GetSyncFileQuery(c *gin.Context) {
	fileType := c.Query("type")
	fileName := c.Query("name")

	log.Printf("Запрос файла (query): type=%s, name=%s", fileType, fileName)

	var state SyncState
	if err := h.db.Where("file_type = ? AND file_name = ?", fileType, fileName).
		First(&state).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": "Файл не найден",
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Файл получен",
		"data": SyncFileResponse{
			FileType:     state.FileType,
			FileName:     state.FileName,
			ZoneName:     state.ZoneName,
			Checksum:     state.Checksum,
			Version:      state.Version,
			LastModified: state.LastModified,
			Content:      state.Content,
		},
	})
}

func (h *SyncHandler) UpdateSyncState(fileType, fileName, zoneName, filePath, changedBy string) (uint, error) {
	checksum, err := calculateChecksum(filePath)
	if err != nil {
		return 0, fmt.Errorf("ошибка вычисления checksum: %v", err)
	}

	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		return 0, fmt.Errorf("ошибка чтения файла: %v", err)
	}

	// Получаем последний номер версии для этого файла
	var lastVersion int
	h.db.Model(&SyncState{}).
		Where("file_type = ? AND file_name = ?", fileType, fileName).
		Select("COALESCE(MAX(version), 0)").
		Scan(&lastVersion)

	newVersion := lastVersion + 1

	// Создаём НОВУЮ запись
	state := SyncState{
		FileType:     fileType,
		FileName:     fileName,
		ZoneName:     zoneName,
		Checksum:     checksum,
		Version:      newVersion,
		Content:      string(content),
		LastModified: time.Now(),
	}

	// Создаём запись и получаем ID
	if err := h.db.Create(&state).Error; err != nil {
		return 0, fmt.Errorf("ошибка сохранения версии: %v", err)
	}

	log.Printf("Создана версия %d (ID=%d) для %s", newVersion, state.ID, fileName)

	return state.ID, nil
}

// GetVersions возвращает список версий файла
func (h *SyncHandler) GetVersions(c *gin.Context) {
	fileType := c.Param("fileType")
	fileName := c.Query("fileName")

	decodedFileName, err := url.QueryUnescape(fileName)
	if err != nil {
		decodedFileName = fileName
	}

	limit := 50
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	var versions []SyncState
	query := h.db.Where("file_type = ? AND file_name = ?", fileType, decodedFileName).
		Order("version DESC").
		Limit(limit)

	if err := query.Find(&versions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Ошибка получения версий",
			"error":   err.Error(),
		})
		return
	}

	// Возвращаем ID в ответе
	type VersionMeta struct {
		ID           uint      `json:"id"`
		Version      int       `json:"version"`
		Checksum     string    `json:"checksum"`
		LastModified time.Time `json:"last_modified"`
		FileName     string    `json:"file_name"`
	}

	metas := make([]VersionMeta, 0, len(versions))
	for _, v := range versions {
		metas = append(metas, VersionMeta{
			ID:           v.ID,
			Version:      v.Version,
			Checksum:     v.Checksum,
			LastModified: v.LastModified,
			FileName:     v.FileName,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Версии получены",
		"data": gin.H{
			"file_type": fileType,
			"file_name": decodedFileName,
			"versions":  metas,
		},
	})
}

// GetVersion возвращает конкретную версию файла
func (h *SyncHandler) GetVersion(c *gin.Context) {
	versionID := c.Param("id")

	var state SyncState
	if err := h.db.First(&state, versionID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": "Версия не найдена",
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Версия получена",
		"data": gin.H{
			"version":       state.Version,
			"file_type":     state.FileType,
			"file_name":     state.FileName,
			"checksum":      state.Checksum,
			"last_modified": state.LastModified,
			"content":       state.Content,
		},
	})
}

// RollbackVersion откатывает файл к указанной версии
func (h *SyncHandler) RollbackVersion(c *gin.Context) {
	versionID := c.Param("id")
	force := c.Query("force") == "true"

	var state SyncState
	if err := h.db.First(&state, versionID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": "Версия не найдена",
			"error":   err.Error(),
		})
		return
	}

	log.Printf("Откат версии %d для %s (type: %s)", state.Version, state.FileName, state.FileType)

	// Блокируем файл
	err := withFileLock(state.FileName, func() error {
		// Записываем контент во временный файл
		tmpPath := state.FileName + ".rollback.tmp"
		if err := ioutil.WriteFile(tmpPath, []byte(state.Content), 0640); err != nil {
			return fmt.Errorf("ошибка записи временного файла: %v", err)
		}

		// Проверяем синтаксис если не force
		if !force {
			var checkCmd string

			// ВАЖНО: Для zone_file используем named-checkzone, для конфигов - named-checkconf
			if state.FileType == "zone_file" {
				// named-checkzone требует имя зоны и путь к файлу
				checkCmd = fmt.Sprintf("named-checkzone %s %s", state.ZoneName, tmpPath)
			} else {
				// Для named_conf и zone_conf
				checkCmd = fmt.Sprintf("named-checkconf %s", tmpPath)
			}

			cmd := exec.Command("bash", "-c", checkCmd)
			output, err := cmd.CombinedOutput()
			if err != nil {
				os.Remove(tmpPath)
				return fmt.Errorf("ошибка синтаксиса: %s - %v", string(output), err)
			}
		}

		// Переименовываем в целевой файл
		if err := os.Rename(tmpPath, state.FileName); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("ошибка переименования: %v", err)
		}

		// Восстанавливаем права
		if state.FileType == "zone_file" {
			exec.Command("chown", "named:named", state.FileName).Run()
			exec.Command("chmod", "644", state.FileName).Run()
		} else {
			exec.Command("chown", "root:named", state.FileName).Run()
			exec.Command("chmod", "640", state.FileName).Run()
		}

		return nil
	})

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Ошибка отката",
			"error":   err.Error(),
		})
		return
	}

	// Перезагружаем BIND
	if err := reloadBind(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Откат выполнен, но reload failed",
			"error":   err.Error(),
		})
		return
	}

	// Логируем операцию
	audit := AuditLog{
		JobType:   "ROLLBACK",
		ZoneName:  state.ZoneName,
		Status:    "COMPLETED",
		CreatedAt: time.Now(),
	}
	db.Create(&audit)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": fmt.Sprintf("Откат к версии %d выполнен", state.Version),
		"data": gin.H{
			"version":   state.Version,
			"file_name": state.FileName,
			"file_type": state.FileType,
			"checksum":  state.Checksum,
		},
	})
}

// DeleteVersion удаляет старую версию (очистка истории)
func (h *SyncHandler) DeleteVersion(c *gin.Context) {
	versionID := c.Param("id")

	var state SyncState
	if err := h.db.First(&state, versionID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": "Версия не найдена",
			"error":   err.Error(),
		})
		return
	}

	// Не удаляем последнюю версию
	var latest SyncState
	if err := h.db.Where("file_type = ? AND file_name = ?", state.FileType, state.FileName).
		Order("version DESC").
		First(&latest).Error; err == nil && latest.ID == state.ID {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Нельзя удалить текущую версию",
		})
		return
	}

	if err := h.db.Delete(&state).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Ошибка удаления версии",
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Версия удалена",
		"data": gin.H{
			"version": state.Version,
		},
	})
}

func calculateChecksum(filePath string) (string, error) {
	// Проверяем существование файла
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return "", nil // Возвращаем пустую строку без ошибки (файл не существует)
	}

	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:]), nil
}

// --- Replica Sync Client (только REPLICA) ---

type StringReplacement struct {
	Pattern     string
	Replacement string
	regex       *regexp.Regexp
}

type ConfigTransform struct {
	MasterIP            string
	ZoneType            string
	ZoneSubdir          string
	RemoveAllowTransfer bool
	AllowTransfer       string
	Replacements        []StringReplacement
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

func NewReplicaSync(masterURL, apiToken string, intervalSeconds int, enabled bool) *ReplicaSync {
	transform := ConfigTransform{
		MasterIP:            os.Getenv("REPLICA_MASTER_IP"),
		ZoneType:            os.Getenv("REPLICA_ZONE_TYPE"),
		ZoneSubdir:          os.Getenv("REPLICA_ZONE_SUBDIR"),
		RemoveAllowTransfer: os.Getenv("REPLICA_REMOVE_ALLOW_TRANSFER") == "true",
		AllowTransfer:       os.Getenv("REPLICA_ALLOW_TRANSFER"),
	}

	if transform.MasterIP == "" {
		transform.MasterIP = "127.0.0.1"
	}
	if transform.ZoneType == "" {
		transform.ZoneType = "slave"
	}

	if replacements := os.Getenv("REPLICA_CONFIG_REPLACEMENTS"); replacements != "" {
		for _, repl := range strings.Split(replacements, "|") {
			parts := strings.SplitN(repl, ":", 2)
			if len(parts) == 2 {
				sr := StringReplacement{
					Pattern:     parts[0],
					Replacement: parts[1],
				}
				sr.regex = regexp.MustCompile(regexp.QuoteMeta(sr.Pattern))
				transform.Replacements = append(transform.Replacements, sr)
			}
		}
	}

	return &ReplicaSync{
		MasterURL:  masterURL,
		APIToken:   apiToken,
		Interval:   time.Duration(intervalSeconds) * time.Second,
		Enabled:    enabled,
		Transform:  transform,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (r *ReplicaSync) Start() {
	if !r.Enabled {
		log.Println("Синхронизация с мастером отключена")
		return
	}

	log.Printf("Запуск синхронизации с мастером %s (интервал: %v)", r.MasterURL, r.Interval)

	go func() {
		ticker := time.NewTicker(r.Interval)
		defer ticker.Stop()

		r.sync()

		for range ticker.C {
			r.sync()
		}
	}()
}

func (r *ReplicaSync) sync() {
	r.mu.Lock()
	if r.isSyncing {
		r.mu.Unlock()
		log.Println("Синхронизация уже выполняется, пропускаем")
		return
	}
	r.isSyncing = true
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.isSyncing = false
		r.mu.Unlock()
	}()

	log.Println("=== Начало синхронизации с мастером ===")
	startTime := time.Now()

	masterState, err := r.getMasterState()
	if err != nil {
		log.Printf("❌ Ошибка получения состояния с мастера: %v", err)
		return
	}

	log.Printf("Получено состояние %d файлов с мастера", len(masterState.Data.Files))

	changedFiles := 0
	for _, fileInfo := range masterState.Data.Files {
		changed, err := r.syncFile(fileInfo)
		if err != nil {
			log.Printf("❌ Ошибка синхронизации файла %s: %v", fileInfo.FileName, err)
			continue
		}
		if changed {
			changedFiles++
			log.Printf("✓ Обновлён: %s (версия %d)", fileInfo.FileName, fileInfo.Version)
		}
	}

	r.mu.Lock()
	r.lastSyncTime = time.Now()
	r.filesUpdatedCount += changedFiles
	r.mu.Unlock()

	elapsed := time.Since(startTime)
	log.Printf("=== Синхронизация завершена за %v: обновлено %d файлов ===", elapsed, changedFiles)

	if changedFiles > 0 {
		if err := r.reloadBIND(); err != nil {
			log.Printf("❌ Ошибка перезагрузки BIND: %v", err)
		} else {
			log.Println("✓ BIND перезапущен успешно")
		}
	}
}

func (r *ReplicaSync) getMasterState() (*ReplicaSyncStateResp, error) {
	req, err := http.NewRequest("GET", r.MasterURL+"/api/sync/state", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Sync-Token", r.APIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса к мастеру: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, fmt.Errorf("ошибка ответа мастера: %d - %s", resp.StatusCode, string(body))
	}

	var state ReplicaSyncStateResp
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа: %v", err)
	}

	return &state, nil
}

func (r *ReplicaSync) saveFileAlreadyTransformed(filePath, content string) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("ошибка создания директории: %v", err)
	}

	tmpPath := filePath + ".tmp"
	if err := ioutil.WriteFile(tmpPath, []byte(content), 0640); err != nil {
		return fmt.Errorf("ошибка записи файла: %v", err)
	}

	// Проверяем синтаксис
	checkCmd := fmt.Sprintf("named-checkconf %s", tmpPath)
	cmd := exec.Command("bash", "-c", checkCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("ошибка синтаксиса: %s - %v", string(output), err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("ошибка переименования: %v", err)
	}

	// Устанавливаем права
	exec.Command("chown", "root:named", filePath).Run()
	exec.Command("chmod", "640", filePath).Run()

	return nil
}

func (r *ReplicaSync) syncConfigFile(fileInfo SyncFileInfo, localPath string) (bool, error) {
	log.Printf("📥 Проверка конфига %s", fileInfo.FileName)

	// 1. Скачиваем контент с мастера
	fileContent, err := r.downloadFile(fileInfo)
	if err != nil {
		return false, err
	}

	// 2. Применяем трансформации
	transformedContent := r.transformConfig(fileContent, fileInfo)

	// 3. Считаем checksum ТРАНСФОРМИРОВАННОГО контента
	transformedChecksum := sha256.Sum256([]byte(transformedContent))
	transformedChecksumHex := hex.EncodeToString(transformedChecksum[:])

	// 4. Считаем checksum ЛОКАЛЬНОГО файла (если существует)
	localChecksum, err := calculateChecksum(localPath)
	fileExists := err == nil && localChecksum != ""

	// 5. Сравниваем checksum трансформированного контента с локальным
	if fileExists && localChecksum == transformedChecksumHex {
		log.Printf("✓ Конфиг %s не изменился (после трансформации)", fileInfo.FileName)
		return false, nil
	}

	log.Printf("📝 Конфиг %s изменился, записываем...", fileInfo.FileName)

	// 6. Записываем только если отличается
	if err := r.saveFileAlreadyTransformed(localPath, transformedContent); err != nil {
		return false, err
	}

	return true, nil
}

func (r *ReplicaSync) syncFile(fileInfo SyncFileInfo) (bool, error) {
	// Пропускаем zone_file — реплика получает зоны через BIND AXFR, не через API
	if fileInfo.FileType == "zone_file" {
		log.Printf("⏭️ Пропускаем зону %s (BIND zone transfer)", fileInfo.ZoneName)
		return false, nil
	}

	// Для конфигов — специальная логика с трансформацией
	if fileInfo.FileType == "named_conf" || fileInfo.FileType == "zone_conf" {
		return r.syncConfigFile(fileInfo, r.getLocalPath(fileInfo))
	}

	// Для остальных файлов — стандартная логика
	localPath := r.getLocalPath(fileInfo)
	localChecksum, err := calculateChecksum(localPath)
	fileExists := (err == nil && localChecksum != "")

	if fileExists && localChecksum == fileInfo.Checksum {
		log.Printf("✓ Файл %s не изменился", fileInfo.FileName)
		return false, nil
	}

	log.Printf("📥 Файл %s изменился", fileInfo.FileName)

	fileContent, err := r.downloadFile(fileInfo)
	if err != nil {
		return false, err
	}

	if err := r.saveFile(localPath, fileContent, fileInfo); err != nil {
		return false, err
	}

	return true, nil
}

func (r *ReplicaSync) downloadFile(fileInfo SyncFileInfo) (string, error) {
	var url1 string
	if fileInfo.FileType == "zone_file" {
		url1 = fmt.Sprintf("%s/api/sync/zone/%s", r.MasterURL, fileInfo.ZoneName)
	} else {
		url1 = fmt.Sprintf("%s/api/sync/file?type=%s&name=%s",
			r.MasterURL,
			fileInfo.FileType,
			url.QueryEscape(fileInfo.FileName))
	}

	req, err := http.NewRequest("GET", url1, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("X-Sync-Token", r.APIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ошибка загрузки файла: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return "", fmt.Errorf("ошибка ответа мастера: %d - %s", resp.StatusCode, string(body))
	}

	var fileResp SyncFileResp
	if err := json.NewDecoder(resp.Body).Decode(&fileResp); err != nil {
		return "", fmt.Errorf("ошибка парсинга ответа: %v", err)
	}

	return fileResp.Data.Content, nil
}

func (r *ReplicaSync) getLocalPath(fileInfo SyncFileInfo) string {
	zoneDir := os.Getenv("BIND_ZONE_DIR")
	if zoneDir == "" {
		zoneDir = "/var/named/"
	}

	switch fileInfo.FileType {
	case "named_conf":
		return os.Getenv("BIND_NAMED_CONF")
	case "zone_conf":
		return os.Getenv("BIND_ZONE_CONF")
	case "zone_file":
		return filepath.Join(zoneDir, filepath.Base(fileInfo.FileName))
	default:
		return filepath.Join(zoneDir, fileInfo.FileName)
	}
}

func (r *ReplicaSync) saveFile(filePath, content string, fileInfo SyncFileInfo) error {
	// Применяем трансформации если это конфиг
	if fileInfo.FileType == "named_conf" || fileInfo.FileType == "zone_conf" {
		content = r.transformConfig(content, fileInfo)
	}

	// ПРОВЕРКА БАЛАНСА СКОБОК
	openBraces := strings.Count(content, "{")
	closeBraces := strings.Count(content, "}")
	if openBraces != closeBraces {
		return fmt.Errorf("нарушен баланс скобок: {=%d, }=%d", openBraces, closeBraces)
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("ошибка создания директории: %v", err)
	}

	tmpPath := filePath + ".tmp"
	if err := ioutil.WriteFile(tmpPath, []byte(content), 0640); err != nil {
		return fmt.Errorf("ошибка записи файла: %v", err)
	}

	// Проверяем синтаксис
	var checkCmd string
	if filepath.Ext(filePath) == ".zone" || filepath.Ext(filePath) == ".rev" {
		zoneName := filepath.Base(filePath)
		checkCmd = fmt.Sprintf("named-checkzone %s %s", zoneName, tmpPath)
	} else {
		checkCmd = fmt.Sprintf("named-checkconf %s", tmpPath)
	}

	cmd := exec.Command("bash", "-c", checkCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("ошибка синтаксиса: %s - %v", string(output), err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("ошибка переименования: %v", err)
	}

	// Устанавливаем права
	if filepath.Ext(filePath) == ".zone" || filepath.Ext(filePath) == ".rev" {
		exec.Command("chown", "named:named", filePath).Run()
		exec.Command("chmod", "644", filePath).Run()
	} else {
		exec.Command("chown", "root:named", filePath).Run()
		exec.Command("chmod", "640", filePath).Run()
	}

	return nil
}

func (r *ReplicaSync) transformConfig(content string, fileInfo SyncFileInfo) string {
	log.Printf("Применение трансформаций к %s", fileInfo.FileName)

	// 1. Трансформация блока options
	content = r.transformOptionsBlock(content)

	// 2. УДАЛЯЕМ allow-update и allow-transfer ИЗ ВСЕГО ФАЙЛА (до трансформации зон)
	// Это гарантирует удаление даже если структура нестандартная
	content = regexp.MustCompile(`(?s)allow-update\s*\{[^}]*\}\s*;`).ReplaceAllString(content, "")
	content = regexp.MustCompile(`(?s)allow-transfer\s*\{[^}]*\}\s*;`).ReplaceAllString(content, "")

	// 3. Трансформация блоков зон (только type, masters, file)
	content = r.transformZoneBlocks(content)

	// 4. Чистка дублирующихся точек с запятой (на случай если что-то пошло не так)
	content = strings.ReplaceAll(content, ";;", ";")

	// 5. Чистка лишних пустых строк
	content = regexp.MustCompile(`\n{3,}`).ReplaceAllString(content, "\n\n")

	log.Printf("Трансформации применены к %s", fileInfo.FileName)
	//log.Printf("Content: \n%s", content)
	return content
}

func (r *ReplicaSync) transformOptionsBlock(content string) string {
	// 1. Отключаем IPv6
	if os.Getenv("REPLICA_DISABLE_IPV6") == "true" {
		content = regexp.MustCompile(`(?m)^\s*listen-on-v6\s+port\s+\d+\s*\{\s*any\s*;\s*\}\s*;`).
			ReplaceAllString(content, "listen-on-v6 port 53 { none; };")
	}

	// 2. Удаляем also-notify
	content = regexp.MustCompile(`(?s)also-notify\s*\{[^}]*\}\s*;`).ReplaceAllString(content, "")

	return content
}

func (r *ReplicaSync) transformZoneBlocks(content string) string {
	zoneRegex := regexp.MustCompile(`(?s)zone\s+"([^"]+)"\s+(?:IN\s+)?\{([^}]+)\}`)

	return zoneRegex.ReplaceAllStringFunc(content, func(match string) string {
		submatch := zoneRegex.FindStringSubmatch(match)
		if len(submatch) < 3 {
			return match
		}

		zoneName := submatch[1]
		zoneBody := submatch[2]

		// Пропускаем специальные зоны
		if zoneName == "." || zoneName == "localhost" || strings.HasPrefix(zoneName, "_") {
			return match
		}

		transformedBody := r.transformZoneBody(zoneBody)

		return fmt.Sprintf(`zone "%s" IN {
%s
};`, zoneName, transformedBody)
	})
}

func (r *ReplicaSync) transformZoneBody(body string) string {
	// 1. Меняем type master на type slave
	body = regexp.MustCompile(`(?m)^\s*type\s+master\s*;`).ReplaceAllString(body, fmt.Sprintf("type %s;", r.Transform.ZoneType))

	// 2. Добавляем masters {}
	if r.Transform.ZoneType == "slave" && r.Transform.MasterIP != "" {
		if !regexp.MustCompile(`(?m)^\s*masters\s*\{`).MatchString(body) {
			body = regexp.MustCompile(`(type\s+\w+\s*;)`).ReplaceAllString(body,
				fmt.Sprintf("$1\n         masters { %s; };", r.Transform.MasterIP))
		}
	}

	// 3. Меняем путь к файлу
	if r.Transform.ZoneSubdir != "" {
		body = regexp.MustCompile(`file\s+"([^/"][^"]*\.zone[^"]*)"`).ReplaceAllStringFunc(body, func(match string) string {
			submatch := regexp.MustCompile(`file\s+"([^"]+)"`).FindStringSubmatch(match)
			if len(submatch) < 2 {
				return match
			}
			filePath := submatch[1]
			if strings.Contains(filePath, "/") || strings.Contains(filePath, r.Transform.ZoneSubdir) {
				return match
			}
			return fmt.Sprintf(`file "%s/%s"`, r.Transform.ZoneSubdir, filePath)
		})
	}

	return body
}

func (r *ReplicaSync) reloadBIND() error {
	cmd := exec.Command("rndc", "reload")
	output, err := cmd.CombinedOutput()
	if err != nil {
		cmd = exec.Command("systemctl", "reload", "named")
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("ошибка перезагрузки BIND: %s - %v", string(output), err)
		}
	}
	return nil
}

func (r *ReplicaSync) GetLastSyncTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSyncTime
}

func (r *ReplicaSync) GetFilesUpdatedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.filesUpdatedCount
}

// --- Handlers ---

func handleStatus(c *gin.Context) {
	cmd := exec.Command("systemctl", "is-active", "named")
	out, err := cmd.CombinedOutput()

	status := "inactive"
	if err == nil && strings.TrimSpace(string(out)) == "active" {
		status = "active"
	}

	response := gin.H{
		"named_status": status,
		"api_version":  "1.0.0",
		"role":         appRole,
	}

	if appRole == "master" {
		sqlDB, _ := db.DB()
		response["db_connected"] = sqlDB.Ping() == nil
		response["queue_size"] = len(jobQueue)
	} else {
		response["master_url"] = os.Getenv("MASTER_URL")
		if replicaSync != nil {
			response["last_sync"] = replicaSync.GetLastSyncTime()
			response["sync_enabled"] = replicaSync.Enabled
		}
	}

	sendResponse(c, http.StatusOK, true, "Статус сервиса", response)
}

func handleConfig(c *gin.Context) {
	zones, _ := parseZoneConfig()

	sendResponse(c, http.StatusOK, true, "Текущая конфигурация", gin.H{
		"zone_dir":    ZoneDir,
		"zone_conf":   ZoneConfFile,
		"named_conf":  NamedConf,
		"db_host":     DbHost,
		"db_name":     DbName,
		"db_schema":   DbSchema,
		"default_ttl": DefaultTTL,
		"api_port":    os.Getenv("API_PORT"),
		"gin_mode":    gin.Mode(),
		"running_as":  os.Geteuid(),
		"go_version":  strings.Replace(runtime.Version(), "go", "", -1),
		"zones_found": len(zones),
		"zones":       zones,
		"queue_size":  len(jobQueue),
	})
}

func handleListZones(c *gin.Context) {
	zones, err := parseZoneConfig()
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка парсинга конфига", err.Error())
		return
	}

	var zoneInfos []ZoneInfo
	for _, zone := range zones {
		count := 0
		if recs, err := readZoneFileSimple(zone.File); err == nil {
			count = len(recs)
		}
		zoneInfos = append(zoneInfos, ZoneInfo{
			Name:        zone.Name,
			File:        zone.File,
			Type:        zone.Type,
			ConfigFile:  zone.ConfigFile,
			RecordCount: count,
		})
	}

	sendResponse(c, http.StatusOK, true, "Список зон", gin.H{"zones": zoneInfos})
}

func handleCreateZone(c *gin.Context) {
	var req ZoneRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		sendResponse(c, http.StatusBadRequest, false, "Ошибка валидации JSON", err.Error())
		return
	}

	job := &Job{
		Type:       JobCreateZone,
		ZoneName:   req.Name,
		Email:      req.Email,
		ConfigFile: req.ConfigFile,
		NsIP:       req.NsIP,
	}

	result, err := submitJob(job)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка очереди", err.Error())
		return
	}

	if result.Success {
		sendResponse(c, http.StatusOK, true, result.Message, result.Data)
	} else {
		sendResponse(c, http.StatusInternalServerError, false, result.Message, result.Error.Error())
	}
}

func handleGetZone(c *gin.Context) {
	zoneName := c.Param("name")
	if !validateZoneName(zoneName) {
		sendResponse(c, http.StatusBadRequest, false, "Недопустимое имя зоны", nil)
		return
	}

	zone, exists := getZoneFromConfig(zoneName)
	if !exists {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена в конфигурации", nil)
		return
	}

	zoneType := "forward"
	if strings.Contains(zoneName, "in-addr.arpa") || strings.Contains(zoneName, "ip6.arpa") {
		zoneType = "reverse"
	}

	records, err := readZoneFileSimple(zone.File)
	if err != nil {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена или не читается", err.Error())
		return
	}

	sendResponse(c, http.StatusOK, true, "Информация о зоне", gin.H{
		"name":         zoneName,
		"type":         zoneType,
		"file":         zone.File,
		"config_file":  zone.ConfigFile,
		"record_count": len(records),
		"records":      records,
	})
}

func handleDeleteZone(c *gin.Context) {
	zoneName := c.Param("name")
	if !validateZoneName(zoneName) {
		sendResponse(c, http.StatusBadRequest, false, "Недопустимое имя зоны", nil)
		return
	}

	job := &Job{
		Type:     JobDeleteZone,
		ZoneName: zoneName,
	}

	result, err := submitJob(job)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка очереди", err.Error())
		return
	}

	if result.Success {
		sendResponse(c, http.StatusOK, true, result.Message, result.Data)
	} else {
		sendResponse(c, http.StatusInternalServerError, false, result.Message, result.Error.Error())
	}
}

func handleAddRecord(c *gin.Context) {
	zoneName := c.Param("name")
	if !validateZoneName(zoneName) {
		sendResponse(c, http.StatusBadRequest, false, "Недопустимое имя зоны", nil)
		return
	}

	var req RecordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		sendResponse(c, http.StatusBadRequest, false, "Ошибка валидации JSON", err.Error())
		return
	}

	job := &Job{
		Type:        JobAddRecord,
		ZoneName:    zoneName,
		RecordName:  req.Name,
		RecordType:  req.Type,
		RecordValue: req.Value,
		TTL:         req.TTL,
		ReversePtr:  req.ReversePtr,
	}

	result, err := submitJob(job)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка очереди", err.Error())
		return
	}

	if result.Success {
		sendResponse(c, http.StatusOK, true, result.Message, result.Data)
	} else {
		sendResponse(c, http.StatusInternalServerError, false, result.Message, result.Error.Error())
	}
}

func handleDeleteRecord(c *gin.Context) {
	zoneName := c.Param("name")
	recordName := c.Param("record")
	recordType := c.Param("type")

	if !validateZoneName(zoneName) {
		sendResponse(c, http.StatusBadRequest, false, "Недопустимое имя зоны", nil)
		return
	}

	if !validateRecordName(recordName) {
		sendResponse(c, http.StatusBadRequest, false, "Недопустимое имя записи", nil)
		return
	}

	job := &Job{
		Type:       JobDeleteRecord,
		ZoneName:   zoneName,
		RecordName: recordName,
		RecordType: recordType,
	}

	result, err := submitJob(job)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка очереди", err.Error())
		return
	}

	if result.Success {
		sendResponse(c, http.StatusOK, true, result.Message, result.Data)
	} else {
		sendResponse(c, http.StatusInternalServerError, false, result.Message, result.Error.Error())
	}
}

func handleReload(c *gin.Context) {
	job := &Job{
		Type: JobReload,
	}

	result, err := submitJob(job)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка очереди", err.Error())
		return
	}

	if result.Success {
		sendResponse(c, http.StatusOK, true, result.Message, result.Data)
	} else {
		sendResponse(c, http.StatusInternalServerError, false, result.Message, result.Error.Error())
	}
}

func handleAuditLog(c *gin.Context) {
	limit := 100
	zoneName := c.Query("zone")
	status := c.Query("status")
	jobType := c.Query("job_type")

	query := db.Model(&AuditLog{})

	if zoneName != "" {
		query = query.Where("zone_name = ?", zoneName)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if jobType != "" {
		query = query.Where("job_type = ?", jobType)
	}

	var logs []AuditLog
	if err := query.Order("created_at DESC").Limit(limit).Find(&logs).Error; err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка чтения аудита", err.Error())
		return
	}

	sendResponse(c, http.StatusOK, true, "Журнал аудита", gin.H{"logs": logs})
}

func handleAuditStats(c *gin.Context) {
	var total, completed, failed int64

	db.Model(&AuditLog{}).Count(&total)
	db.Model(&AuditLog{}).Where("status = ?", "COMPLETED").Count(&completed)
	db.Model(&AuditLog{}).Where("status = ?", "FAILED").Count(&failed)

	successRate := float64(0)
	if total > 0 {
		successRate = float64(completed) / float64(total) * 100
	}

	sendResponse(c, http.StatusOK, true, "Статистика аудита", gin.H{
		"total":        total,
		"completed":    completed,
		"failed":       failed,
		"success_rate": successRate,
	})
}

func handleReplicaStatus(c *gin.Context) {
	sendResponse(c, http.StatusOK, true, "REPLICA статус", gin.H{
		"role":          "replica",
		"master_url":    os.Getenv("MASTER_URL"),
		"sync_interval": os.Getenv("SYNC_INTERVAL"),
		"last_sync":     replicaSync.GetLastSyncTime(),
		"sync_enabled":  replicaSync.Enabled,
	})
}

func handleReplicaLastUpdate(c *gin.Context) {
	sendResponse(c, http.StatusOK, true, "Последнее обновление", gin.H{
		"last_sync":     replicaSync.GetLastSyncTime(),
		"files_updated": replicaSync.GetFilesUpdatedCount(),
	})
}

func loggerMiddleware() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return fmt.Sprintf("[BIND-API] %s | %3d | %13v | %15s | %-7s %s\n",
			param.TimeStamp.Format("2006/01/02 - 15:04:05"),
			param.StatusCode,
			param.Latency,
			param.ClientIP,
			param.Method,
			param.Path,
		)
	})
}

func main() {
	// Загрузка .env
	if err := godotenv.Load(); err != nil {
		log.Println("WARNING: .env файл не найден")
	}

	initConfig()

	// Определяем роль
	appRole = os.Getenv("APP_ROLE")
	if appRole == "" {
		appRole = "master"
	}
	log.Printf("=== РОЛЬ СЕРВЕРА: %s ===", strings.ToUpper(appRole))

	// Инициализация БД (только MASTER)
	if err := initDatabase(); err != nil {
		log.Fatalf("Ошибка инициализации БД: %v", err)
	}

	// Инициализация обработчика синхронизации (только MASTER)
	if appRole == "master" {
		syncHandler = NewSyncHandler(db)
		log.Println("✓ Синхронизация MASTER инициализирована")

		initJobQueue()
		log.Println("✓ Очередь заданий инициализирована")
	}

	// Инициализация клиента синхронизации (только REPLICA)
	if appRole == "replica" {
		masterURL := os.Getenv("MASTER_URL")
		apiToken := os.Getenv("MASTER_API_TOKEN")
		syncInterval := 30
		if val := os.Getenv("SYNC_INTERVAL"); val != "" {
			syncInterval, _ = strconv.Atoi(val)
		}

		if masterURL == "" {
			log.Fatal("ERROR: MASTER_URL не указан для REPLICA")
		}

		replicaSync = NewReplicaSync(masterURL, apiToken, syncInterval, true)
		replicaSync.Start()
		log.Println("✓ Синхронизация REPLICA запущена")
	}

	// Проверка BIND
	if _, err := exec.LookPath("rndc"); err != nil {
		log.Fatal("Утилита rndc не найдена в PATH")
	}

	if _, err := os.Stat(ZoneDir); os.IsNotExist(err) {
		log.Fatalf("Директория зон не существует: %s", ZoneDir)
	}

	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(loggerMiddleware())
	r.Use(gin.Recovery())

	api := r.Group("/api")
	{
		api.GET("/status", handleStatus)

		// Endpoints для синхронизации (ТОЛЬКО MASTER)
		if appRole == "master" {
			sync0 := api.Group("/sync", syncHandler.syncAuthMiddleware())
			{
				sync0.GET("/state", syncHandler.GetSyncState)
				sync0.GET("/state/:fileType/:fileName", syncHandler.GetSyncFile)
				sync0.GET("/zones", syncHandler.GetSyncZones)
				sync0.GET("/zone/:zoneName", syncHandler.GetSyncZone)
				sync0.GET("/file", syncHandler.GetSyncFileQuery)

				// Работа с версиями
				sync0.GET("/versions/:fileType", syncHandler.GetVersions)
				sync0.GET("/version/:id", syncHandler.GetVersion)
				sync0.POST("/version/:id/rollback", syncHandler.RollbackVersion)
				sync0.DELETE("/version/:id", syncHandler.DeleteVersion)
			}

			// Обычные API endpoints (ТОЛЬКО MASTER)
			api.GET("/config", handleConfig)
			api.GET("/audit", handleAuditLog)
			api.GET("/audit/stats", handleAuditStats)
			api.POST("/reload", handleReload)
			api.GET("/zones", handleListZones)
			api.POST("/zone", handleCreateZone)

			zones := api.Group("/zone/:name")
			{
				zones.GET("", handleGetZone)
				zones.DELETE("", handleDeleteZone)
				zones.POST("/record", handleAddRecord)
				zones.DELETE("/record/:record/:type", handleDeleteRecord)
			}
		} else {
			// REPLICA - только статус и информация о синхронизации
			api.GET("/sync/status", handleReplicaStatus)
			api.GET("/sync/last-update", handleReplicaLastUpdate)
		}
	}

	port := os.Getenv("API_PORT")
	if port == "" {
		port = ":8080"
	}

	log.Printf("BIND Manager API запущен на порту %s", port)
	log.Printf("Режим: %s", appRole)

	if appRole == "master" {
		log.Printf("База данных: %s@%s:%s/%s", DbUser, DbHost, DbPort, DbName)
	} else {
		log.Printf("MASTER URL: %s", os.Getenv("MASTER_URL"))
	}

	if err := r.Run(port); err != nil {
		log.Fatal(err)
	}
}
