package internal

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func InitConfig() {
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

func InitDatabase() error {
	// На REPLICA пропускаем инициализацию БД
	if AppRole == "replica" {
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
		Db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Info),
		})
		if err != nil {
			log.Printf("Ошибка подключения (попытка %d/%d): %v", i+1, maxRetries, err)
			time.Sleep(2 * time.Second)
			continue
		}

		sqlDB, err := Db.DB()
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

	log.Println("Выполнение миграций базы данных...")
	if err := Db.AutoMigrate(&AuditLog{}, &SyncState{}, &APIKey{}); err != nil {
		return fmt.Errorf("ошибка миграции: %v", err)
	}

	if err != nil {
		return fmt.Errorf("не удалось подключиться к PostgreSQL после %d попыток: %v", maxRetries, err)
	}
	var count int64
	Db.Model(&APIKey{}).Count(&count)
	if count == 0 {
		bootstrapKey := strings.TrimSpace(os.Getenv("BIND_API_BOOTSTRAP_KEY"))
		if bootstrapKey == "" {
			log.Printf("WARNING: таблица api_keys пуста, bootstrap API-ключ не создан: переменная BIND_API_BOOTSTRAP_KEY не задана")
		} else {
			if len(bootstrapKey) < 32 {
				return fmt.Errorf("значение BIND_API_BOOTSTRAP_KEY слишком короткое: минимум 32 символа")
			}

			if len(bootstrapKey) > 120 {
				return fmt.Errorf("значение BIND_API_BOOTSTRAP_KEY слишком длинное: максимум 120 символов")
			}

			expiresAt := time.Now().Add(7 * 24 * time.Hour)
			permsJSON, _ := json.Marshal([]string{"*"})
			defaultKey := &APIKey{
				Key:         bootstrapKey,
				Name:        "bootstrap-admin",
				Description: "Временный bootstrap ключ из переменной окружения, срок действия 7 дней",
				Permissions: string(permsJSON),
				ExpiresAt:   &expiresAt,
			}

			if err := Db.Create(defaultKey).Error; err != nil {
				log.Printf("WARNING: Не удалось создать bootstrap API-ключ из переменной окружения: %v", err)
			} else {
				log.Printf("✓ Bootstrap API-ключ создан из переменной окружения BIND_API_BOOTSTRAP_KEY со сроком действия 7 дней")
			}
		}
	}

	log.Println("База данных PostgreSQL инициализирована")
	return nil
}

// queueMonitor отслеживает размер очереди и переключает режимы
func queueMonitor() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		queueSize := len(JQ)
		queuePercent := float64(queueSize) / float64(MaxQueueSize)

		ModeMutex.RLock()
		currentMode := CurrentMode
		ModeMutex.RUnlock()

		if currentMode == "normal" && queuePercent >= QueueThresholdHigh {
			ModeMutex.Lock()
			CurrentMode = "batch"
			ModeMutex.Unlock()
			log.Printf("🔄 Переключение в BATCH режим (очередь: %.1f%%, %d/%d)",
				queuePercent*100, queueSize, MaxQueueSize)

			// Принудительный сброс накопленных заданий
			select {
			case BatchFlushCh <- struct{}{}:
			default:
			}
		} else if currentMode == "batch" && queuePercent <= QueueThresholdLow {
			ModeMutex.Lock()
			CurrentMode = "normal"
			ModeMutex.Unlock()
			log.Printf("🔄 Переключение в NORMAL режим (очередь: %.1f%%, %d/%d)",
				queuePercent*100, queueSize, MaxQueueSize)

			// Сбрасываем накопленный пакет
			flushBatch()
		}
	}
}

// adaptiveWorker обрабатывает задания в зависимости от режима
func adaptiveWorker() {
	for job := range JQ {
		ModeMutex.RLock()
		mode := CurrentMode
		ModeMutex.RUnlock()

		if mode == "normal" {
			// Normal режим: обрабатываем сразу
			processJob(job)
		} else {
			// Batch режим: накапливаем задания
			addToBatch(job)
		}
	}
}

// addToBatch добавляет задание в пакет
func addToBatch(job *Job) {
	BatchMutex.Lock()
	defer BatchMutex.Unlock()

	BatchJobs = append(BatchJobs, job)

	// Если набрали достаточно заданий или получили сигнал сброса
	if len(BatchJobs) >= BatchSize {
		go flushBatch()
	}
}

// flushBatch сбрасывает накопленные задания и применяет их пакетом
func flushBatch() {
	BatchMutex.Lock()
	if len(BatchJobs) == 0 {
		BatchMutex.Unlock()
		return
	}

	// Копируем задания
	jobs := make([]*Job, len(BatchJobs))
	copy(jobs, BatchJobs)
	BatchJobs = BatchJobs[:0] // Очищаем
	BatchMutex.Unlock()

	if len(jobs) == 0 {
		return
	}

	log.Printf("📦 Применение пакета из %d заданий", len(jobs))
	startTime := time.Now()

	// Группируем задания по зонам
	jobsByZone := make(map[string][]*Job)
	for _, job := range jobs {
		jobsByZone[job.ZoneName] = append(jobsByZone[job.ZoneName], job)
	}

	// Применяем задания для каждой зоны
	for zoneName, zoneJobs := range jobsByZone {
		zone, exists := getZoneFromConfig(zoneName)
		if !exists {
			log.Printf("Зона %s не найдена, пропускаем %d заданий", zoneName, len(zoneJobs))
			continue
		}

		// Блокируем файл зоны на время пакетной обработки
		err := withFileLock(zone.File, func() error {
			for _, job := range zoneJobs {
				// Применяем задание без отдельной блокировки
				switch job.Type {
				case JobAddRecord:
					applyAddRecordToFile(job, zone)
				case JobDeleteRecord:
					applyDeleteRecordToFile(job, zone)
				}
			}
			// Один раз увеличиваем serial для всех изменений в зоне
			if err := incrementSerial(zone.File); err != nil {
				log.Printf("Ошибка обновления serial для зоны %s: %v", zoneName, err)
			}
			return nil
		})

		if err != nil {
			log.Printf("Ошибка пакетной обработки зоны %s: %v", zoneName, err)
			// Отмечаем задания как упавшие
			for _, job := range zoneJobs {
				logAudit(job, "FAILED", err.Error())
				job.ResponseCh <- JobResult{Success: false, Error: err}
				close(job.ResponseCh)
			}
			continue
		}

		// Все задания успешно выполнены
		for _, job := range zoneJobs {
			logAudit(job, "COMPLETED", "")
			job.ResponseCh <- JobResult{Success: true, Message: "Запись добавлена (batch)"}
			close(job.ResponseCh)
		}

		fixPermissions(zone.File)
	}

	// Отмечаем что нужен reload
	PendingReload = true

	elapsed := time.Since(startTime)
	log.Printf("✅ Пакет из %d заданий применён за %v", len(jobs), elapsed)
}

// applyAddRecordToFile применяет добавление записи к файлу (без блокировок)
func applyAddRecordToFile(job *Job, zone *ZoneConfig) error {
	ttl := job.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}

	recordLine := fmt.Sprintf("%s\t%d\tIN\t%s\t%s",
		job.RecordName, ttl, strings.ToUpper(job.RecordType), job.RecordValue)

	return appendRecordToFile(zone.File, recordLine)
}

// applyDeleteRecordToFile применяет удаление записи из файла (без блокировок)
func applyDeleteRecordToFile(job *Job, zone *ZoneConfig) error {
	return deleteRecordFromFile(zone.File, job.RecordName, strings.ToUpper(job.RecordType))
}

// batchReloadWorker выполняет периодический reload в batch режиме
func batchReloadWorker() {
	ticker := time.NewTicker(ReloadInterval)
	defer ticker.Stop()

	for range ticker.C {
		if PendingReload {
			if err := reloadBind(); err != nil {
				log.Printf("❌ Периодический reload failed: %v", err)
			} else {
				log.Printf("🔄 Периодический reload BIND выполнен")
				PendingReload = false
			}
		}
	}
}

func InitJobQueue() {
	JQ = make(chan *Job, MaxQueueSize)
	BatchJobs = make([]*Job, 0, BatchSize)
	BatchFlushCh = make(chan struct{}, 1)

	// Запускаем главный воркер
	go adaptiveWorker()

	// Запускаем мониторинг очереди для переключения режимов
	go queueMonitor()

	// Запускаем периодический reload для batch-режима
	go batchReloadWorker()

	// Разбор очередей по таймеру
	go batchFlushTimer()

	log.Printf("Адаптивная очередь заданий инициализирована")
}

func batchFlushTimer() {
	ticker := time.NewTicker(BatchInterval)
	defer ticker.Stop()

	for range ticker.C {
		BatchMutex.Lock()
		jobCount := len(BatchJobs)
		BatchMutex.Unlock()

		if jobCount > 0 {
			flushBatch()
		}
	}
}

func jobWorker() {
	for job := range JQ {
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
	JQMutex.Lock()
	if len(JQ) >= MaxQueueSize {
		JQMutex.Unlock()
		return nil, fmt.Errorf("очередь переполнена")
	}
	JQMutex.Unlock()

	job.ResponseCh = make(chan JobResult, 1)
	job.CreatedAt = time.Now()

	var lastID uint
	Db.Model(&AuditLog{}).Select("COALESCE(MAX(id), 0)").Scan(&lastID)
	job.ID = int64(lastID) + 1

	JQ <- job

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

	if err := Db.Create(&audit).Error; err != nil {
		log.Printf("WARNING: Не удалось записать аудит: %v", err)
	}
}

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

// ensureReverseZoneExists проверяет существование обратной зоны и создаёт её если нужно
func ensureReverseZoneExists(ip string, email string, nsIP string) error {
	reverseZoneName, err := getReverseZoneName(ip)
	if err != nil {
		return fmt.Errorf("не удалось получить имя обратной зоны: %v", err)
	}

	log.Printf("Проверка обратной зоны: %s для IP %s", reverseZoneName, ip)

	// Проверяем существует ли зона в конфиге
	if zoneExistsInConfig(reverseZoneName) {
		log.Printf("Обратная зона %s уже существует", reverseZoneName)
		return nil
	}

	log.Printf("Обратная зона %s не найдена, создаём...", reverseZoneName)

	// ЖЕСТКО ИСПОЛЬЗУЕМ NamedConf
	targetConfigFile := NamedConf

	// Создаём имя файла зоны
	zoneFile := filepath.Join(ZoneDir, reverseZoneName+".rev")

	// Проверяем что файл ещё не существует
	if _, err := os.Stat(zoneFile); err == nil {
		log.Printf("Файл зоны %s уже существует, пропускаем создание", zoneFile)
		return nil
	}

	// Формируем email для SOA
	if email == "" {
		email = "admin." + reverseZoneName
	}
	soaEmail := strings.Replace(email, "@", ".", -1)
	if !strings.HasSuffix(soaEmail, ".") {
		soaEmail += "."
	}

	// Если nsIP не задан — берём первый доступный IP сервера
	if nsIP == "" {
		serverIPs := getServerIPs()
		if len(serverIPs) > 0 {
			nsIP = serverIPs[0]
		} else {
			nsIP = "127.0.0.1"
		}
	}

	// Генерируем серийный номер
	now := time.Now()
	serial := fmt.Sprintf("%d%02d%02d01", now.Year(), now.Month(), now.Day())

	// Создаём контент зоны
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
`, DefaultTTL, reverseZoneName, soaEmail, serial, DefaultRefresh, DefaultRetry, DefaultExpire, DefaultNegative, reverseZoneName, DefaultTTL, nsIP)

	// Записываем файл зоны
	err = withFileLock(zoneFile, func() error {
		return os.WriteFile(zoneFile, []byte(zoneContent), 0644)
	})
	if err != nil {
		return fmt.Errorf("не удалось создать файл обратной зоны: %v", err)
	}

	// Устанавливаем права
	if err := fixPermissions(zoneFile); err != nil {
		return fmt.Errorf("ошибка прав доступа: %v", err)
	}

	// Добавляем зону в КОНФИГ (NamedConf)
	zoneConfig := fmt.Sprintf(`
zone "%s" IN {
         type master;
         file "%s";
         allow-update { none; };
};
`, reverseZoneName, filepath.Base(zoneFile))

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
		return fmt.Errorf("ошибка записи в конфиг %s: %v", targetConfigFile, err)
	}

	// Устанавливаем права на конфиг
	exec.Command("chown", "root:named", targetConfigFile).Run()
	exec.Command("chmod", "640", targetConfigFile).Run()

	// Проверяем синтаксис
	cmd := exec.Command("named-checkconf")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ошибка в конфигурации: %s", string(out))
	}

	cmd = exec.Command("named-checkzone", reverseZoneName, zoneFile)
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ошибка в файле зоны: %s", string(out))
	}

	log.Printf("Обратная зона %s создана: файл=%s, конфиг=%s", reverseZoneName, zoneFile, targetConfigFile)

	// Обновляем состояние для синхронизации
	if SH != nil {
		SH.UpdateSyncState("named_conf", NamedConf, "", NamedConf, "api")
		SH.UpdateSyncState("zone_conf", ZoneConfFile, "", ZoneConfFile, "api")
		SH.UpdateSyncState("zone_file", zoneFile, reverseZoneName, zoneFile, "api")
	}

	return nil
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

func executeCreateZone(job *Job) JobResult {
	start := time.Now()
	var err error
	defer func() {
		if Metrics != nil {
			Metrics.RecordOperation("create_zone", time.Since(start), err)
		}
	}()

	if !validateZoneName(job.ZoneName) {
		err = fmt.Errorf("недопустимое имя зоны")
		return JobResult{Success: false, Error: err}
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
		err = fmt.Errorf("зона уже существует")
		return JobResult{Success: false, Error: err}
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
		err = fmt.Errorf("файл зоны уже существует")
		return JobResult{Success: false, Error: err}
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

	err = withFileLock(zoneFile, func() error {
		return os.WriteFile(zoneFile, []byte(zoneContent), 0644)
	})
	if err != nil {
		err = fmt.Errorf("не удалось создать файл зоны: %v", err)
		return JobResult{Success: false, Error: err}
	}

	if err := fixPermissions(zoneFile); err != nil {
		err = fmt.Errorf("ошибка прав доступа: %v", err)
		return JobResult{Success: false, Error: err}
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
		err = fmt.Errorf("ошибка записи в конфиг: %v", err)
		return JobResult{Success: false, Error: err}
	}

	cmd := exec.Command("chown", "root:named", targetConfigFile)
	_ = cmd.Run()
	cmd = exec.Command("chmod", "640", targetConfigFile)
	_ = cmd.Run()

	cmd = exec.Command("named-checkconf")
	out, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("ошибка в конфигурации: %s", string(out))
		return JobResult{Success: false, Error: err}
	}

	cmd = exec.Command("named-checkzone", job.ZoneName, zoneFile)
	out, err = cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("ошибка в файле зоны: %s", string(out))
		return JobResult{Success: false, Error: err}
	}

	// Новая логика reload
	ModeMutex.RLock()
	currentMode := CurrentMode
	ModeMutex.RUnlock()

	if currentMode == "normal" {
		if err := reloadBind(); err != nil {
			log.Printf("WARNING: reload после создания зоны %s не выполнен: %v", job.ZoneName, err)
		} else {
			log.Printf("✓ Reload выполнен после создания зоны %s", job.ZoneName)
		}
	} else {
		PendingReload = true
		log.Printf("📦 Batch режим: создана зона %s, reload будет выполнен позже", job.ZoneName)
	}

	// Обновляем состояние для синхронизации
	if SH != nil {
		SH.UpdateSyncState("named_conf", NamedConf, "", NamedConf, "api")
		SH.UpdateSyncState("zone_conf", ZoneConfFile, "", ZoneConfFile, "api")
		SH.UpdateSyncState("zone_file", zoneFile, job.ZoneName, zoneFile, "api")
	}

	// Обновляем бизнес-метрики после создания зоны
	if Metrics != nil {
		go Metrics.UpdateBusinessMetrics()
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
	start := time.Now()
	var err error
	defer func() {
		if Metrics != nil {
			Metrics.RecordOperation("delete_zone", time.Since(start), err)
		}
	}()

	if !validateZoneName(job.ZoneName) {
		err = fmt.Errorf("недопустимое имя зоны")
		return JobResult{Success: false, Error: err}
	}

	zone, exists := getZoneFromConfig(job.ZoneName)
	if !exists {
		err = fmt.Errorf("зона не найдена в конфигурации")
		return JobResult{Success: false, Error: err}
	}

	log.Printf("Удаление зоны %s: файл=%s, конфиг=%s", job.ZoneName, zone.File, zone.ConfigFile)

	// Удаляем файл зоны
	err = withFileLock(zone.File, func() error {
		if _, err := os.Stat(zone.File); err == nil {
			return os.Remove(zone.File)
		}
		return nil
	})
	if err != nil {
		err = fmt.Errorf("не удалось удалить файл зоны: %v", err)
		return JobResult{Success: false, Error: err}
	}

	// Удаляем зону из конфига
	err = withFileLock(zone.ConfigFile, func() error {
		return removeZoneFromConfig(zone.ConfigFile, job.ZoneName)
	})
	if err != nil {
		err = fmt.Errorf("ошибка удаления зоны из конфига: %v", err)
		return JobResult{Success: false, Error: err}
	}

	// Проверяем синтаксис конфига
	cmd := exec.Command("named-checkconf")
	out, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("ошибка в конфигурации: %s", string(out))
		return JobResult{Success: false, Error: err}
	}

	// Удаляем записи о зоне из sync_states (БД мастера)
	if Db != nil {
		result := Db.Where("file_type = ? AND zone_name = ?", "zone_file", job.ZoneName).
			Delete(&SyncState{})

		if result.Error != nil {
			log.Printf("⚠️ Ошибка удаления зоны %s из sync_states: %v", job.ZoneName, result.Error)
		} else {
			log.Printf("🗑️ Зона %s удалена из sync_states (удалено %d записей)",
				job.ZoneName, result.RowsAffected)
		}
	}

	// Отмечаем что нужен reload
	PendingReload = true

	// Обновляем состояние для синхронизации (удаляем из конфигов)
	if SH != nil {
		SH.UpdateSyncState("named_conf", NamedConf, "", NamedConf, "api")
		SH.UpdateSyncState("zone_conf", ZoneConfFile, "", ZoneConfFile, "api")
	}

	// Обновляем бизнес-метрики после удаления зоны
	if Metrics != nil {
		go Metrics.UpdateBusinessMetrics()
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
	start := time.Now()
	var err error
	defer func() {
		if Metrics != nil {
			Metrics.RecordOperation("add_record", time.Since(start), err)
		}
	}()

	// Валидация имени зоны
	if !validateZoneName(job.ZoneName) {
		err = fmt.Errorf("недопустимое имя зоны: %s", job.ZoneName)
		return JobResult{Success: false, Error: err}
	}

	// Валидация имени записи
	if !validateRecordName(job.RecordName) {
		err = fmt.Errorf("недопустимое имя записи: %s", job.RecordName)
		return JobResult{Success: false, Error: err}
	}

	// Валидация типа записи
	recordType := strings.ToUpper(job.RecordType)
	validTypes := map[string]bool{
		"A": true, "AAAA": true, "CNAME": true,
		"MX": true, "TXT": true, "NS": true,
	}
	if !validTypes[recordType] {
		err = fmt.Errorf("неподдерживаемый тип записи: %s (поддерживаются: A, AAAA, CNAME, MX, TXT, NS)", recordType)
		return JobResult{Success: false, Error: err}
	}

	// Валидация значения записи
	if err = validateRecordValue(recordType, job.RecordValue); err != nil {
		return JobResult{Success: false, Error: err}
	}

	// Валидация TTL
	if job.TTL != 0 {
		if err = validateTTL(job.TTL); err != nil {
			return JobResult{Success: false, Error: err}
		}
	}

	// Проверка существования зоны
	zone, exists := getZoneFromConfig(job.ZoneName)
	if !exists {
		err = fmt.Errorf("зона %s не найдена в конфигурации", job.ZoneName)
		return JobResult{Success: false, Error: err}
	}

	// Проверка существования файла зоны
	if _, err := os.Stat(zone.File); os.IsNotExist(err) {
		err = fmt.Errorf("файл зоны не существует: %s", zone.File)
		return JobResult{Success: false, Error: err}
	}

	// Проверка на дубликат (опционально, можно убрать для скорости)
	// if err = validateDuplicateRecord(zone.File, job.RecordName, recordType, job.RecordValue); err != nil {
	// 	return JobResult{Success: false, Error: err}
	// }

	// Специальные проверки для CNAME записей
	if recordType == "CNAME" {
		records, _ := readZoneFileSimple(zone.File)
		for _, rec := range records {
			if rec.Name == job.RecordName && rec.Type != "CNAME" {
				err = fmt.Errorf("невозможно добавить CNAME запись для %s: уже существует запись типа %s", job.RecordName, rec.Type)
				return JobResult{Success: false, Error: err}
			}
		}
	}

	// Специальные проверки для MX записей
	if recordType == "MX" {
		parts := strings.Fields(job.RecordValue)
		if len(parts) == 2 {
			mxHostname := parts[1]
			records, _ := readZoneFileSimple(zone.File)
			for _, rec := range records {
				if rec.Name == mxHostname && rec.Type == "CNAME" {
					err = fmt.Errorf("MX запись не может указывать на CNAME: %s", mxHostname)
					return JobResult{Success: false, Error: err}
				}
			}
		}
	}

	// Специальные проверки для NS записей
	if recordType == "NS" {
		records, _ := readZoneFileSimple(zone.File)
		for _, rec := range records {
			if rec.Name == job.RecordValue && rec.Type == "CNAME" {
				err = fmt.Errorf("NS запись не может указывать на CNAME: %s", job.RecordValue)
				return JobResult{Success: false, Error: err}
			}
		}
	}

	ttl := job.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}

	// Формируем строку записи
	var recordLine string
	if recordType == "MX" {
		parts := strings.Fields(job.RecordValue)
		if len(parts) == 2 {
			recordLine = fmt.Sprintf("%s\t%d\tIN\tMX\t%s\t%s",
				job.RecordName, ttl, parts[0], parts[1])
		} else {
			err = fmt.Errorf("неверный формат MX записи")
			return JobResult{Success: false, Error: err}
		}
	} else {
		recordLine = fmt.Sprintf("%s\t%d\tIN\t%s\t%s",
			job.RecordName, ttl, recordType, job.RecordValue)
	}

	log.Printf("📝 Асинхронное добавление записи в зону %s: %s", job.ZoneName, recordLine)

	// === АСИНХРОННАЯ ЗАПИСЬ ===
	// Запись добавляется в буфер, ответ возвращается мгновенно
	if RecordBuffer != nil {
		RecordBuffer.Add(job.ZoneName, recordLine)
	} else {
		// Fallback на синхронную запись если буфер не инициализирован
		err = withFileLock(zone.File, func() error {
			if err := appendRecordToFile(zone.File, recordLine); err != nil {
				return fmt.Errorf("ошибка добавления записи: %v", err)
			}
			if err := incrementSerial(zone.File); err != nil {
				return fmt.Errorf("ошибка обновления Serial: %v", err)
			}
			return nil
		})
		if err != nil {
			return JobResult{Success: false, Error: err}
		}
	}

	// === АВТО-СОЗДАНИЕ ОБРАТНОЙ ЗОНЫ И PTR ЗАПИСИ (асинхронно) ===
	if (recordType == "A" || recordType == "AAAA") && job.RecordValue != "" {
		go func() {
			zoneEmail := "admin." + job.ZoneName
			zoneNsIP := ""
			serverIPs := getServerIPs()
			if len(serverIPs) > 0 {
				zoneNsIP = serverIPs[0]
			}

			// Создаем обратную зону если нужно
			if err := ensureReverseZoneExists(job.RecordValue, zoneEmail, zoneNsIP); err != nil {
				log.Printf("WARNING: Не удалось создать обратную зону для %s: %v", job.RecordValue, err)
			}

			// Добавляем PTR запись
			ptrName := job.ReversePtr
			if ptrName == "" {
				ptrName = job.RecordName + "." + job.ZoneName
				if !strings.HasSuffix(ptrName, ".") {
					ptrName += "."
				}
			} else if !strings.HasSuffix(ptrName, ".") {
				ptrName += "."
			}

			if err := addPtrRecord(job.RecordValue, ptrName, ttl); err != nil {
				log.Printf("WARNING: Не удалось создать PTR запись: %v", err)
			}
		}()
	}

	// Обновляем состояние для синхронизации (асинхронно)
	if SH != nil {
		go SH.UpdateSyncState("zone_file", zone.File, job.ZoneName, zone.File, "api")
	}

	// Обновляем метрики записей (асинхронно)
	if Metrics != nil {
		go Metrics.UpdateBusinessMetrics()
	}

	log.Printf("✓ Запись %s типа %s принята в буфер для зоны %s (асинхронно)",
		job.RecordName, recordType, job.ZoneName)

	return JobResult{
		Success: true,
		Message: fmt.Sprintf("Запись %s типа %s принята в очередь (будет активирована в течение %v)",
			job.RecordName, recordType, BatchInterval),
		Data: gin.H{
			"zone":        job.ZoneName,
			"record":      job.RecordName,
			"type":        recordType,
			"value":       job.RecordValue,
			"ttl":         ttl,
			"async":       true,
			"flush_delay": BatchInterval.String(),
		},
	}
}

func executeDeleteRecord(job *Job) JobResult {
	start := time.Now()
	var err error
	defer func() {
		if Metrics != nil {
			Metrics.RecordOperation("delete_record", time.Since(start), err)
		}
	}()

	if !validateZoneName(job.ZoneName) {
		err = fmt.Errorf("недопустимое имя зоны")
		return JobResult{Success: false, Error: err}
	}

	if !validateRecordName(job.RecordName) {
		err = fmt.Errorf("недопустимое имя записи")
		return JobResult{Success: false, Error: err}
	}

	zone, exists := getZoneFromConfig(job.ZoneName)
	if !exists {
		err = fmt.Errorf("зона не найдена в конфигурации")
		return JobResult{Success: false, Error: err}
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

	err = withFileLock(zone.File, func() error {
		if err := deleteRecordFromFile(zone.File, job.RecordName, recordType); err != nil {
			return err
		}
		return incrementSerial(zone.File)
	})
	if err != nil {
		err = fmt.Errorf("ошибка удаления записи: %v", err)
		return JobResult{Success: false, Error: err}
	}

	if err := fixPermissions(zone.File); err != nil {
		err = fmt.Errorf("ошибка прав: %v", err)
		return JobResult{Success: false, Error: err}
	}

	// Новая логика reload
	ModeMutex.RLock()
	currentMode := CurrentMode
	ModeMutex.RUnlock()

	if currentMode == "normal" {
		if err := reloadBind(); err != nil {
			log.Printf("WARNING: reload после удаления записи %s не выполнен: %v", job.RecordName, err)
		} else {
			log.Printf("✓ Reload выполнен после удаления записи %s", job.RecordName)
		}
	} else {
		PendingReload = true
		log.Printf("📦 Batch режим: удалена запись %s, reload будет выполнен позже", job.RecordName)
	}

	// Обновляем состояние для синхронизации
	if SH != nil {
		SH.UpdateSyncState("zone_file", zone.File, job.ZoneName, zone.File, "api")
	}

	// Обновляем метрики записей
	if Metrics != nil {
		go Metrics.UpdateBusinessMetrics()
	}

	return JobResult{Success: true, Message: "Запись удалена"}
}

func executeReload() JobResult {
	start := time.Now()
	var err error
	defer func() {
		if Metrics != nil {
			Metrics.RecordOperation("reload", time.Since(start), err)
		}
	}()

	if err := reloadBind(); err != nil {
		return JobResult{Success: false, Error: err}
	}
	return JobResult{Success: true, Message: "BIND перезагружен"}
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

// --- Sync Handler (только MASTER) ---

func NewSH(db *gorm.DB) *SyncHandler {
	return &SyncHandler{db: db}
}

func (h *SyncHandler) SyncAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		log.Println("SyncAuthMiddleware run")
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
			ID:           state.ID,
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

	decodedFileName, err := url.QueryUnescape(fileName)
	if err != nil {
		decodedFileName = fileName
	}

	log.Printf("Запрос файла: type=%s, name=%s (decoded: %s)", fileType, fileName, decodedFileName)

	var state SyncState
	// ✅ ДОБАВЛЕНО: Order("version DESC")
	if err := h.db.Where("file_type = ? AND file_name = ?", fileType, decodedFileName).
		Order("version DESC").
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
	var zones []string

	// Возвращаем только уникальные имена зон
	err := h.db.Table("sync_states").
		Where("file_type = ?", "zone_file").
		Distinct("zone_name").
		Pluck("zone_name", &zones).Error

	if err != nil {
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
			"zones":     zones,
			"count":     len(zones),
			"timestamp": time.Now(),
		},
	})
}

func (h *SyncHandler) GetSyncZone(c *gin.Context) {
	zoneName := c.Param("zoneName")
	var state SyncState

	// ✅ ДОБАВЛЕНО: Order("version DESC")
	if err := h.db.Where("file_type = ? AND zone_name = ?", "zone_file", zoneName).
		Order("version DESC").
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
	// ✅ ДОБАВЛЕНО: Order("version DESC")
	if err := h.db.Where("file_type = ? AND file_name = ?", fileType, fileName).
		Order("version DESC").
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

	switch state.FileType {
	case "zone_file", "named_conf", "zone_conf":
	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Неподдерживаемый тип файла для rollback",
		})
		return
	}

	if state.FileType == "zone_file" && !validateZoneName(state.ZoneName) {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Некорректное имя зоны для rollback",
		})
		return
	}

	// Блокируем файл
	err := withFileLock(state.FileName, func() error {
		// Записываем контент во временный файл
		tmpPath := state.FileName + ".rollback.tmp"
		if err := os.WriteFile(tmpPath, []byte(state.Content), 0640); err != nil {
			return fmt.Errorf("ошибка записи временного файла: %v", err)
		}

		// Проверяем синтаксис если не force
		if !force {
			var cmd *exec.Cmd

			switch state.FileType {
			case "zone_file":
				cmd = exec.Command("named-checkzone", state.ZoneName, tmpPath)
			case "named_conf", "zone_conf":
				cmd = exec.Command("named-checkconf", tmpPath)
			default:
				os.Remove(tmpPath)
				return fmt.Errorf("неподдерживаемый тип файла для rollback: %s", state.FileType)
			}

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
	Db.Create(&audit)

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

// --- Replica Sync Client (только REPLICA) ---

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
		MasterURL:  strings.TrimRight(masterURL, "/"),
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

	// Существующая синхронизация файлов
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
	log.Printf("=== Синхронизация файлов завершена за %v: обновлено %d файлов ===", elapsed, changedFiles)

	// НОВЫЙ КОД: Проверка зон и их A записей
	log.Println("=== Начинаем проверку зон и A записей ===")
	if err := r.CheckAndFixZones(); err != nil {
		log.Printf("❌ Ошибка при проверке зон: %v", err)
	} else {
		log.Println("=== Проверка зон завершена ===")
	}

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
	// Регулярка захватывает весь блок зоны
	zoneRegex := regexp.MustCompile(`(?s)zone\s+"([^"]+)"\s+(?:IN\s+)?\{([^}]+)\}`)

	return zoneRegex.ReplaceAllStringFunc(content, func(match string) string {
		submatch := zoneRegex.FindStringSubmatch(match)
		if len(submatch) < 3 {
			return match
		}

		zoneName := submatch[1]
		zoneBody := submatch[2]

		// Пропускаем специальные зоны (корневая, localhost, _tcp и т.п.)
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
	// 1. Определяем исходный тип зоны
	originalType := ""
	typeMatch := regexp.MustCompile(`(?m)^\s*type\s+(\w+)\s*;`).FindStringSubmatch(body)
	if len(typeMatch) >= 2 {
		originalType = strings.ToLower(typeMatch[1])
	}

	// 2. Пропускаем специальные типы зон (forward, hint, stub, delegate)
	// Их не нужно трансформировать в slave
	if originalType == "forward" || originalType == "hint" || originalType == "stub" || originalType == "delegate" {
		log.Printf("⏭️ Пропускаем трансформацию зоны типа '%s'", originalType)
		return body
	}

	// 3. Меняем type master на type slave (только для мастер-зон)
	if originalType == "master" && r.Transform.ZoneType != "" {
		body = regexp.MustCompile(`(?m)^\s*type\s+master\s*;`).ReplaceAllString(body, fmt.Sprintf("type %s;", r.Transform.ZoneType))
	}

	// 4. Добавляем masters {} только если:
	//    - исходный тип был master
	//    - целевой тип slave
	//    - masters ещё нет в зоне
	if originalType == "master" && r.Transform.ZoneType == "slave" && r.Transform.MasterIP != "" {
		if !regexp.MustCompile(`(?m)^\s*masters\s*\{`).MatchString(body) {
			body = regexp.MustCompile(`(type\s+slave\s*;)`).ReplaceAllString(body,
				fmt.Sprintf("$1\n         masters { %s; };", r.Transform.MasterIP))
		}
	}

	// 5. Меняем путь к файлу (только относительные пути)
	if r.Transform.ZoneSubdir != "" && originalType == "master" {
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

	// 6. Удаляем allow-update (не нужно для slave)
	body = regexp.MustCompile(`(?s)\s*allow-update\s*\{[^}]*\}\s*;`).ReplaceAllString(body, "")

	// 7. Удаляем allow-transfer (если задано)
	if r.Transform.RemoveAllowTransfer {
		body = regexp.MustCompile(`(?s)\s*allow-transfer\s*\{[^}]*\}\s*;`).ReplaceAllString(body, "")
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

// APIKeyAuth проверяет API-ключ и права доступа
func APIKeyAuth(requiredPerm string) gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetHeader("X-API-Key")
		if apiKey == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Ошибка авторизации",
			})
			c.Abort()
			return
		}

		// Проверяем что Db не nil
		if Db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"message": "Внутренняя ошибка сервера: база данных не инициализирована",
			})
			c.Abort()
			return
		}

		var key APIKey
		if err := Db.Where("key = ?", apiKey).First(&key).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Неверный API-ключ",
			})
			c.Abort()
			return
		}

		if key.IsExpired() {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "API-ключ истёк",
			})
			c.Abort()
			return
		}

		if requiredPerm != "" && !key.HasPermission(requiredPerm) {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": "Недостаточно прав: требуется " + requiredPerm,
			})
			c.Abort()
			return
		}

		if key.IPAddress != "" {
			clientIP := c.ClientIP()
			if clientIP != key.IPAddress {
				c.JSON(http.StatusForbidden, gin.H{
					"success": false,
					"message": "Доступ запрещён с этого IP-адреса",
				})
				c.Abort()
				return
			}
		}

		// Асинхронно обновляем время последнего использования
		go func(keyID uint) {
			if Db != nil {
				now := time.Now()
				Db.Model(&APIKey{}).Where("id = ?", keyID).Update("last_used_at", now)
			}
		}(key.ID)

		c.Set("api_key_id", key.ID)
		c.Set("api_key_name", key.Name)

		c.Next()
	}
}

// StartNamedConfWatcher запускает фоновую задачу для отслеживания изменений named.conf
func StartNamedConfWatcher() {
	if AppRole != "master" {
		return
	}

	log.Println("🔄 Запуск мониторинга изменений /etc/named.conf (интервал: 30 сек)")

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		// Первая проверка сразу
		syncNamedConf()

		for range ticker.C {
			syncNamedConf()
		}
	}()
}

// syncNamedConf проверяет изменения в named.conf и сохраняет в БД если есть изменения
func syncNamedConf() {
	filePath := NamedConf
	if filePath == "" {
		filePath = DefaultNamedConf
	}

	// Проверяем существование файла
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Printf("⚠️  Файл %s не существует, пропускаем синхронизацию", filePath)
		return
	}

	// Вычисляем текущий checksum
	currentChecksum, err := calculateChecksum(filePath)
	if err != nil {
		log.Printf("❌ Ошибка вычисления checksum для %s: %v", filePath, err)
		return
	}

	// Получаем последний checksum из БД
	var lastState SyncState
	if err := Db.Where("file_type = ? AND file_name = ?", "named_conf", filePath).
		Order("version DESC").
		First(&lastState).Error; err != nil {
		// Если записей нет - сохраняем первую версию
		if err == gorm.ErrRecordNotFound {
			log.Printf("📝 Первая версия %s, сохраняем в БД", filePath)
			saveNamedConfVersion(filePath, currentChecksum)
			return
		}
		log.Printf("❌ Ошибка получения последней версии: %v", err)
		return
	}

	// Сравниваем checksum
	if lastState.Checksum == currentChecksum {
		// Изменений нет - пропускаем
		return
	}

	// Изменения есть - сохраняем новую версию
	log.Printf("📝 Обнаружены изменения в %s, сохраняем версию %d", filePath, lastState.Version+1)
	saveNamedConfVersion(filePath, currentChecksum)
}

// saveNamedConfVersion сохраняет версию named.conf в БД
func saveNamedConfVersion(filePath, checksum string) {
	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		log.Printf("❌ Ошибка чтения файла %s: %v", filePath, err)
		return
	}

	// Получаем последний номер версии
	var lastVersion int
	Db.Model(&SyncState{}).
		Where("file_type = ? AND file_name = ?", "named_conf", filePath).
		Select("COALESCE(MAX(version), 0)").
		Scan(&lastVersion)

	newVersion := lastVersion + 1

	state := SyncState{
		FileType:     "named_conf",
		FileName:     filePath,
		ZoneName:     "",
		Checksum:     checksum,
		Version:      newVersion,
		Content:      string(content),
		LastModified: time.Now(),
	}

	if err := Db.Create(&state).Error; err != nil {
		log.Printf("❌ Ошибка сохранения версии в БД: %v", err)
		return
	}

	log.Printf("✅ Сохранена версия %d для %s (checksum: %s...)", newVersion, filePath, checksum[:16])
}

// CheckARecordResolve проверяет, резолвится ли A запись через DNS реплики
func (r *ReplicaSync) CheckARecordResolve(zoneName, recordName, recordValue string) bool {
	// Формируем полное доменное имя
	var fqdn string
	if recordName == "@" {
		fqdn = zoneName
	} else if strings.HasSuffix(recordName, zoneName) {
		fqdn = recordName
	} else {
		fqdn = recordName + "." + zoneName
	}

	// Убираем точку в конце если есть
	fqdn = strings.TrimSuffix(fqdn, ".")

	replicaIP := os.Getenv("REPLICA_EXTERNAL_IP")
	if replicaIP == "" {
		replicaIP = "127.0.0.1"
	}

	log.Printf("Проверка резолвинга %s через реплику %s, ожидается %s", fqdn, replicaIP, recordValue)

	// Выполняем nslookup: запрашиваем A-запись (тип 1) у указанного DNS-сервера [citation:5]
	cmd := exec.Command("nslookup", "-type=A", fqdn, replicaIP)
	output, err := cmd.CombinedOutput()

	if err != nil {
		log.Printf("Ошибка при выполнении nslookup для %s: %v", fqdn, err)
		return false
	}

	// Парсим вывод для извлечения IP-адреса
	resolvedIP := parseNslookupForIP(string(output))
	if resolvedIP == "" {
		log.Printf("Не удалось извлечь IP-адрес из вывода nslookup для %s", fqdn)
		return false
	}

	if resolvedIP == recordValue {
		log.Printf("✓ Запись %s успешно резолвится в %s", fqdn, resolvedIP)
		return true
	}

	log.Printf("✗ Запись %s не резолвится (ожидалось %s, получено %s)", fqdn, recordValue, resolvedIP)
	return false
}

// parseNslookupForIP извлекает IPv4-адрес из стандартного вывода утилиты nslookup
func parseNslookupForIP(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		// Игнорируем строки, которые не содержат информации об адресе
		if !strings.Contains(line, "Address") && !strings.Contains(line, "address") {
			continue
		}

		// Пропускаем строку с адресом DNS-сервера (обычно "Address: 100.69.13.4#53")
		if strings.Contains(line, "#") {
			continue
		}

		// Пытаемся найти IPv4-адрес в строке.
		// Используем простой подход: ищем любую последовательность из 4 чисел, разделенных точками.
		// Это основной, но достаточный для большинства случаев метод.
		parts := strings.Fields(line)
		for _, part := range parts {
			if IsIPv4(part) {
				return part
			}
		}
	}
	return ""
}

// GetMasterZonesList получает список всех зон с мастера
func (r *ReplicaSync) GetMasterZonesList() ([]string, error) {
	urlZ := fmt.Sprintf("%s/api/sync/zones", r.MasterURL)

	req, err := http.NewRequest("GET", urlZ, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Sync-Token", r.APIToken)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса к мастеру: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("мастер вернул код %d: %s", resp.StatusCode, string(body))
	}

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Zones []string `json:"zones"`
			Count int      `json:"count"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа: %v", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("мастер вернул ошибку")
	}

	return response.Data.Zones, nil
}

// GetZoneARecordsFromMaster получает все A записи зоны с мастера через API
func (r *ReplicaSync) GetZoneARecordsFromMaster(zoneName string) ([]RecordInfo, error) {
	urlZ := fmt.Sprintf("%s/api/sync/zone/%s/records", r.MasterURL, zoneName)

	req, err := http.NewRequest("GET", urlZ, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Sync-Token", r.APIToken)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("мастер вернул код %d", resp.StatusCode)
	}

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Records []RecordInfo `json:"records"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}

	if !response.Success {
		return nil, fmt.Errorf("мастер вернул ошибку")
	}

	return response.Data.Records, nil
}

// CheckAndFixZones проверяет все зоны и делает retransfer если нужно
func (r *ReplicaSync) CheckAndFixZones() error {
	if !r.Enabled {
		return nil
	}

	// Получаем список всех зон с мастера (теперь это []string)
	zones, err := r.GetMasterZonesList()
	if err != nil {
		return fmt.Errorf("ошибка получения списка зон: %v", err)
	}

	log.Printf("Проверка %d зон на реплике", len(zones))

	for _, zoneName := range zones {
		// Получаем все A записи зоны с мастера
		records, err := r.GetZoneARecordsFromMaster(zoneName)
		if err != nil {
			log.Printf("Ошибка получения A записей для зоны %s: %v", zoneName, err)
			continue
		}

		// Проверяем каждую A запись
		needRetransfer := false
		for _, record := range records {
			resolved := r.CheckARecordResolve(zoneName, record.Name, record.Value)
			if Metrics != nil {
				Metrics.RecordReplicaCheck(zoneName, resolved)
			}
			if !resolved {
				needRetransfer = true
				break
			}
		}

		// Если хотя бы одна запись не резолвится - делаем retransfer
		if needRetransfer {
			log.Printf("Обнаружены проблемы с резолвингом зоны %s, выполняем retransfer", zoneName)

			cmd := exec.Command("rndc", "retransfer", zoneName)
			output, err := cmd.CombinedOutput()

			if Metrics != nil {
				Metrics.RecordReplicaRetransfer(zoneName, err)
			}

			if err != nil {
				log.Printf("Ошибка retransfer для зоны %s: %v, output: %s", zoneName, err, string(output))
				continue
			}

			log.Printf("Retransfer для зоны %s выполнен успешно", zoneName)

			// После retransfer делаем паузу и проверяем снова
			time.Sleep(2 * time.Second)

			// Повторная проверка после retransfer
			allResolved := true
			for _, record := range records {
				if !r.CheckARecordResolve(zoneName, record.Name, record.Value) {
					allResolved = false
					log.Printf("После retransfer запись %s.%s всё ещё не резолвится", record.Name, zoneName)
				}
			}

			if allResolved {
				log.Printf("✓ Зона %s полностью синхронизирована", zoneName)
			} else {
				log.Printf("⚠ После retransfer зона %s всё ещё имеет проблемы", zoneName)
			}
		}
	}

	return nil
}

// InitQueueConfig инициализирует конфигурацию очереди из переменных окружения
func InitQueueConfig() {
	// MaxQueueSize
	if val := os.Getenv("MAX_QUEUE_SIZE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil && i > 0 {
			MaxQueueSize = i
		}
	}
	if MaxQueueSize == 0 {
		MaxQueueSize = DefaultMaxQueueSize
	}

	// WorkerTimeout
	if val := os.Getenv("WORKER_TIMEOUT"); val != "" {
		if i, err := strconv.Atoi(val); err == nil && i > 0 {
			WorkerTimeout = time.Duration(i) * time.Second
		}
	}
	if WorkerTimeout == 0 {
		WorkerTimeout = DefaultWorkerTimeout
	}

	// BatchSize
	if val := os.Getenv("BATCH_SIZE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil && i > 0 {
			BatchSize = i
		}
	}
	if BatchSize == 0 {
		BatchSize = DefaultBatchSize
	}

	// BatchInterval
	if val := os.Getenv("BATCH_INTERVAL"); val != "" {
		if i, err := strconv.Atoi(val); err == nil && i > 0 {
			BatchInterval = time.Duration(i) * time.Second
		}
	}
	if BatchInterval == 0 {
		BatchInterval = DefaultBatchInterval
	}

	// QueueThresholdLow
	if val := os.Getenv("QUEUE_THRESHOLD_LOW"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil && f > 0 {
			QueueThresholdLow = f
		}
	}
	if QueueThresholdLow == 0 {
		QueueThresholdLow = DefaultQueueThresholdLow
	}

	// QueueThresholdHigh
	if val := os.Getenv("QUEUE_THRESHOLD_HIGH"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil && f > 0 {
			QueueThresholdHigh = f
		}
	}
	if QueueThresholdHigh == 0 {
		QueueThresholdHigh = DefaultQueueThresholdHigh
	}

	// ReloadInterval
	if val := os.Getenv("RELOAD_INTERVAL"); val != "" {
		if i, err := strconv.Atoi(val); err == nil && i > 0 {
			ReloadInterval = time.Duration(i) * time.Second
		}
	}
	if ReloadInterval == 0 {
		ReloadInterval = DefaultReloadInterval
	}

	BatchFlushCh = make(chan struct{}, 1)
	BatchJobs = make([]*Job, 0, BatchSize)
	CurrentMode = "normal"

	log.Printf("Очередь инициализирована: MaxSize=%d, BatchSize=%d, BatchInterval=%v, ThresholdLow=%.0f%%, ThresholdHigh=%.0f%%",
		MaxQueueSize, BatchSize, BatchInterval, QueueThresholdLow*100, QueueThresholdHigh*100)
}

// CleanupOrphanSyncStates удаляет из БД записи о зонах, которых нет в конфиге мастера
func CleanupOrphanSyncStates() {
	if AppRole != "master" || Db == nil {
		return
	}

	log.Println("🧹 Мастер: проверка sync_states на наличие удаленных зон...")

	// Получаем актуальные зоны из конфига мастера
	currentZones, err := parseZoneConfig()
	if err != nil {
		log.Printf("❌ Ошибка получения текущих зон: %v", err)
		return
	}

	// Создаем map существующих зон
	existingZones := make(map[string]bool)
	for _, zone := range currentZones {
		existingZones[zone.Name] = true
	}

	// Получаем все зоны из БД мастера
	var dbZones []string
	err = Db.Table("sync_states").
		Where("file_type = ?", "zone_file").
		Where("zone_name IS NOT NULL AND zone_name != ''").
		Distinct("zone_name").
		Pluck("zone_name", &dbZones).Error

	if err != nil {
		log.Printf("❌ Ошибка получения зон из БД: %v", err)
		return
	}

	// Удаляем записи о зонах, которых нет в конфиге мастера
	deletedCount := 0
	for _, zoneName := range dbZones {
		if !existingZones[zoneName] {
			result := Db.Where("file_type = ? AND zone_name = ?", "zone_file", zoneName).
				Delete(&SyncState{})

			if result.Error != nil {
				log.Printf("⚠️ Ошибка удаления зоны %s из sync_states: %v", zoneName, result.Error)
			} else if result.RowsAffected > 0 {
				log.Printf("🗑️ Удалена зона %s из sync_states (удалено %d записей)",
					zoneName, result.RowsAffected)
				deletedCount++
			}
		}
	}

	if deletedCount > 0 {
		log.Printf("✅ Очистка SyncStates завершена. Удалено зон: %d", deletedCount)
	} else {
		log.Printf("✅ Очистка SyncStates завершена. Удаленных зон не найдено")
	}
}

// StartSyncStateCleaner запускает периодическую очистку sync_states на мастере
func StartSyncStateCleaner() {
	if AppRole != "master" {
		return
	}

	interval := 5 * time.Minute // По умолчанию раз в 5 минут

	if val := os.Getenv("SYNC_CLEANUP_INTERVAL"); val != "" {
		if i, err := time.ParseDuration(val); err == nil {
			interval = i
		}
	}

	log.Printf("🧹 Запуск очистки SyncStates на мастере (интервал: %v)", interval)

	go func() {
		// Первая очистка через 1 минуту после старта
		time.Sleep(1 * time.Minute)
		CleanupOrphanSyncStates()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			CleanupOrphanSyncStates()
		}
	}()
}
