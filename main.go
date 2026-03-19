package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
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
	ZoneDir        string
	ZoneConfFile   string
	NamedConf      string
	DbHost         string
	DbPort         string
	DbUser         string
	DbPassword     string
	DbName         string
	DbSSLMode      string
	DbURL          string
	DbSchema       string
	db             *gorm.DB
	jobQueue       chan *Job
	jobQueueMutex  sync.Mutex
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
	DefaultDbUser       = "dns"
	DefaultDbName       = "dns"
	DefaultDbSSLMode    = "disable"
	DefaultDbSchema     = "bind_api"
	DefaultTTL          = 3600
	DefaultRefresh      = 3600
	DefaultRetry        = 600
	DefaultExpire       = 604800
	DefaultNegative     = 3600
	MaxQueueSize        = 1000
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

// TableName указывает имя таблицы в БД
func (AuditLog) TableName() string {
	// ✅ Если схема не public, добавляем её к имени таблицы
	if DbSchema != "" && DbSchema != "public" {
		return fmt.Sprintf("%s.audit_logs", DbSchema)
	}
	return "audit_logs"
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

	log.Printf("Конфигурация: ZoneDir=%s, ZoneConfFile=%s, NamedConf=%s",
		ZoneDir, ZoneConfFile, NamedConf)
	log.Printf("PostgreSQL: Host=%s, Port=%s, User=%s, DB=%s Scheme=%s",
		DbHost, DbPort, DbUser, DbName, DbSchema)
}

// --- Инициализация БД с GORM ---

func initDatabase() error {
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

	// Пробуем подключиться с повторами
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

		// Проверяем соединение
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

		// Настраиваем пул соединений
		sqlDB.SetMaxOpenConns(25)
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(5 * time.Minute)

		log.Println("Успешное подключение к PostgreSQL")
		break
	}

	if err != nil {
		return fmt.Errorf("не удалось подключиться к PostgreSQL после %d попыток: %v", maxRetries, err)
	}

	// Автоматическая миграция моделей
	log.Println("Выполнение миграций базы данных...")
	if err := db.AutoMigrate(&AuditLog{}); err != nil {
		return fmt.Errorf("ошибка миграции: %v", err)
	}

	log.Println("База данных PostgreSQL инициализирована (миграции выполнены)")
	return nil
}

func initJobQueue() {
	jobQueue = make(chan *Job, MaxQueueSize)
	go jobWorker()
	log.Println("Очередь заданий инициализирована")
}

// --- Очередь заданий ---

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
		result = executeReload(job)
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

	// Получаем последний ID через GORM
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

// --- Аудит с GORM ---

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

// --- Выполнители заданий ---

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

	return JobResult{Success: true, Message: "Запись удалена"}
}

func executeReload(job *Job) JobResult {
	if err := reloadBind(); err != nil {
		return JobResult{Success: false, Error: err}
	}
	return JobResult{Success: true, Message: "BIND перезагружен"}
}

// --- Handlers ---

func handleStatus(c *gin.Context) {
	cmd := exec.Command("systemctl", "is-active", "named")
	out, err := cmd.CombinedOutput()

	status := "inactive"
	if err == nil && strings.TrimSpace(string(out)) == "active" {
		status = "active"
	}

	// Проверка подключения к БД
	sqlDB, _ := db.DB()
	dbConnected := sqlDB.Ping() == nil

	sendResponse(c, http.StatusOK, true, "Статус сервиса", gin.H{
		"named_status": status,
		"api_version":  "1.0.0",
		"queue_size":   len(jobQueue),
		"db_connected": dbConnected,
	})
}

func handleConfig(c *gin.Context) {
	zones, _ := parseZoneConfig()

	sendResponse(c, http.StatusOK, true, "Текущая конфигурация", gin.H{
		"zone_dir":    ZoneDir,
		"zone_conf":   ZoneConfFile,
		"named_conf":  NamedConf,
		"db_host":     DbHost,
		"db_name":     DbName,
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

	// Построение запроса с GORM
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
	// Статистика с GORM
	var total int64
	var completed int64
	var failed int64

	db.Model(&AuditLog{}).Count(&total)
	db.Model(&AuditLog{}).Where("status = ?", "COMPLETED").Count(&completed)
	db.Model(&AuditLog{}).Where("status = ?", "FAILED").Count(&failed)

	sendResponse(c, http.StatusOK, true, "Статистика аудита", gin.H{
		"total":        total,
		"completed":    completed,
		"failed":       failed,
		"success_rate": float64(completed) / float64(total) * 100,
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
	// Загрузка переменных из .env
	if err := godotenv.Load(); err != nil {
		log.Println("WARNING: .env файл не найден, используем переменные окружения")
	}
	initConfig()

	if err := initDatabase(); err != nil {
		log.Fatalf("Ошибка инициализации БД: %v", err)
	}

	initJobQueue()

	if os.Geteuid() != 0 {
		log.Println("WARNING: Сервис запущен не от root. Возможны ошибки записи в системные директории")
	}

	if _, err := exec.LookPath("rndc"); err != nil {
		log.Fatal("Утилита rndc не найдена в PATH. Установите bind-utils")
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
	}

	port := os.Getenv("API_PORT")
	if port == "" {
		port = ":8080"
	}

	log.Printf("BIND Manager API запущен на порту %s", port)
	log.Printf("Используемые пути: ZoneDir=%s, ZoneConfFile=%s, NamedConf=%s", ZoneDir, ZoneConfFile, NamedConf)

	if err := r.Run(port); err != nil {
		log.Fatal(err)
	}
}
