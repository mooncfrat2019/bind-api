package internal

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// --- Handlers ---

func HandleStatus(c *gin.Context) {
	status := checkBindStatus()

	response := gin.H{
		"named_status": status,
		"api_version":  "1.0.0",
		"role":         AppRole,
		"environment":  getEnvironment(),
	}

	if AppRole == "master" {
		if Db != nil {
			sqlDB, _ := Db.DB()
			if sqlDB != nil {
				response["db_connected"] = sqlDB.Ping() == nil
			} else {
				response["db_connected"] = false
			}
		} else {
			response["db_connected"] = false
		}
		response["queue_size"] = len(JQ)

		// Добавляем информацию о режиме очереди
		ModeMutex.RLock()
		response["queue_mode"] = CurrentMode
		ModeMutex.RUnlock()

		response["pending_reload"] = PendingReload

		// Информация о BIND процессе
		if bindInfo := getBindProcessInfo(); bindInfo != nil {
			response["bind_process"] = bindInfo
		}
	} else {
		response["master_url"] = os.Getenv("MASTER_URL")
		if RS != nil {
			response["last_sync"] = RS.GetLastSyncTime()
			response["sync_enabled"] = RS.Enabled
			response["files_updated"] = RS.GetFilesUpdatedCount()
		}
	}

	sendResponse(c, http.StatusOK, true, "Статус сервиса", response)
}

func HandleConfig(c *gin.Context) {
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
		"queue_size":  len(JQ),
	})
}

func HandleListZones(c *gin.Context) {
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

func HandleCreateZone(c *gin.Context) {
	var req ZoneRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		sendResponse(c, http.StatusBadRequest, false, "Ошибка валидации JSON", err.Error())
		return
	}

	// Валидация имени зоны
	if !validateZoneName(req.Name) {
		sendResponse(c, http.StatusBadRequest, false,
			"Недопустимое имя зоны",
			"Имя зоны должно содержать только буквы, цифры, дефисы и точки, длина от 1 до 253 символов")
		return
	}

	// Валидация обратной зоны если нужно
	if strings.Contains(req.Name, "in-addr.arpa") || strings.Contains(req.Name, "ip6.arpa") {
		if !validateReverseZoneName(req.Name) {
			sendResponse(c, http.StatusBadRequest, false,
				"Недопустимое имя обратной зоны",
				"Неверный формат обратной зоны. Пример для IPv4: 1.168.192.in-addr.arpa")
			return
		}
	}

	// Валидация email
	if req.Email != "" {
		if !strings.Contains(req.Email, "@") || len(req.Email) > 255 {
			sendResponse(c, http.StatusBadRequest, false,
				"Недопустимый email адрес",
				"Email должен содержать @ и быть не длиннее 255 символов")
			return
		}

		// Проверка на недопустимые символы в email
		emailParts := strings.Split(req.Email, "@")
		if len(emailParts) != 2 {
			sendResponse(c, http.StatusBadRequest, false,
				"Недопустимый email адрес",
				"Email должен содержать ровно один символ @")
			return
		}

		localPart := emailParts[0]
		domain := emailParts[1]

		if len(localPart) == 0 || len(domain) == 0 {
			sendResponse(c, http.StatusBadRequest, false,
				"Недопустимый email адрес",
				"Локальная часть и домен не могут быть пустыми")
			return
		}
	}

	// Валидация NS IP
	if req.NsIP != "" {
		ip := net.ParseIP(req.NsIP)
		if ip == nil {
			sendResponse(c, http.StatusBadRequest, false,
				"Недопустимый IP адрес для NS записи",
				"Укажите корректный IPv4 или IPv6 адрес")
			return
		}

		// Для reverse зон проверяем соответствие IP
		if strings.Contains(req.Name, "in-addr.arpa") {
			if ip.To4() == nil {
				sendResponse(c, http.StatusBadRequest, false,
					"Несоответствие IP адреса",
					"Для обратной зоны IPv4 необходимо указать IPv4 адрес")
				return
			}
		}
	}

	// Проверка существования зоны
	if zoneExistsInConfig(req.Name) {
		sendResponse(c, http.StatusConflict, false,
			"Зона уже существует",
			fmt.Sprintf("Зона %s уже существует в конфигурации", req.Name))
		return
	}

	// Проверка имени файла
	zoneType := "forward"
	if strings.Contains(req.Name, "in-addr.arpa") || strings.Contains(req.Name, "ip6.arpa") {
		zoneType = "reverse"
	}

	var zoneFileName string
	if zoneType == "reverse" {
		zoneFileName = req.Name + ".rev"
	} else {
		zoneFileName = req.Name + ".zone"
	}

	zoneFilePath := filepath.Join(ZoneDir, zoneFileName)

	// Проверка что файл не существует
	if _, err := os.Stat(zoneFilePath); err == nil {
		sendResponse(c, http.StatusConflict, false,
			"Файл зоны уже существует",
			fmt.Sprintf("Файл %s уже существует", zoneFilePath))
		return
	}

	// Определяем конфиг файл для добавления зоны
	targetConfigFile := req.ConfigFile
	if targetConfigFile == "" {
		zones, err := parseZoneConfig()
		if err != nil || len(zones) == 0 {
			targetConfigFile = NamedConf
		} else {
			targetConfigFile = zones[0].ConfigFile
		}
	}

	// Проверяем что конфиг файл существует и доступен для записи
	if _, err := os.Stat(targetConfigFile); os.IsNotExist(err) {
		sendResponse(c, http.StatusBadRequest, false,
			"Конфигурационный файл не найден",
			fmt.Sprintf("Файл %s не существует", targetConfigFile))
		return
	}

	// Создаем задание
	job := &Job{
		Type:       JobCreateZone,
		ZoneName:   req.Name,
		Email:      req.Email,
		ConfigFile: targetConfigFile,
		NsIP:       req.NsIP,
	}

	// Отправляем в очередь
	result, err := submitJob(job)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false,
			"Ошибка очереди заданий",
			err.Error())
		return
	}

	if result.Success {
		// Дополнительная информация в ответе
		responseData := gin.H{
			"zone":        req.Name,
			"type":        zoneType,
			"config_file": targetConfigFile,
			"zone_file":   zoneFilePath,
		}

		if result.Data != nil {
			if data, ok := result.Data.(gin.H); ok {
				for k, v := range data {
					responseData[k] = v
				}
			}
		}

		sendResponse(c, http.StatusOK, true, result.Message, responseData)
	} else {
		sendResponse(c, http.StatusInternalServerError, false,
			result.Message,
			result.Error.Error())
	}
}

func HandleGetZone(c *gin.Context) {
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

func HandleDeleteZone(c *gin.Context) {
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

func HandleAddRecord(c *gin.Context) {
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

func HandleDeleteRecord(c *gin.Context) {
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

func HandleReload(c *gin.Context) {
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

func HandleAuditLog(c *gin.Context) {
	limit := 100
	zoneName := c.Query("zone")
	status := c.Query("status")
	jobType := c.Query("job_type")

	query := Db.Model(&AuditLog{})

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

func HandleAuditStats(c *gin.Context) {
	var total, completed, failed, started int64

	Db.Model(&AuditLog{}).Count(&total)
	Db.Model(&AuditLog{}).Where("status = ?", "COMPLETED").Count(&completed)
	Db.Model(&AuditLog{}).Where("status = ?", "FAILED").Count(&failed)
	Db.Model(&AuditLog{}).Where("status = ?", "STARTED").Count(&started)

	successRate := float64(0)
	if total > 0 {
		successRate = float64(completed) / (float64(total) - float64(started)) * 100
	}

	sendResponse(c, http.StatusOK, true, "Статистика аудита", gin.H{
		"total":        total,
		"started":      started,
		"completed":    completed,
		"failed":       failed,
		"success_rate": successRate,
	})
}

func HandleReplicaStatus(c *gin.Context) {
	sendResponse(c, http.StatusOK, true, "REPLICA статус", gin.H{
		"role":          "replica",
		"master_url":    os.Getenv("MASTER_URL"),
		"sync_interval": os.Getenv("SYNC_INTERVAL"),
		"last_sync":     RS.GetLastSyncTime(),
		"sync_enabled":  RS.Enabled,
	})
}

func HandleReplicaLastUpdate(c *gin.Context) {
	sendResponse(c, http.StatusOK, true, "Последнее обновление", gin.H{
		"last_sync":     RS.GetLastSyncTime(),
		"files_updated": RS.GetFilesUpdatedCount(),
	})
}

// HandleCreateAPIKey создаёт новый API-ключ
func HandleCreateAPIKey(c *gin.Context) {
	var req CreateAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		sendResponse(c, http.StatusBadRequest, false, "Ошибка валидации: "+err.Error(), nil)
		return
	}

	// Валидация прав
	if len(req.Permissions) == 0 {
		sendResponse(c, http.StatusBadRequest, false, "Требуется хотя бы одно право", nil)
		return
	}

	// ГЕНЕРАЦИЯ КЛЮЧА
	plainKey := generateSecureKey()

	// ХЕШИРОВАНИЕ
	keyHash, err := hashAPIKey(plainKey)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка хеширования ключа", nil)
		return
	}

	// Создаём ключ в БД
	permsJSON, err := json.Marshal(req.Permissions)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка сериализации прав", nil)
		return
	}

	var expiresAt *time.Time
	if req.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(req.ExpiresIn) * time.Hour)
		expiresAt = &exp
	}

	apiKey := &APIKey{
		Key:         keyHash,
		KeyHash:     keyHash,
		KeyPrefix:   generateKeyPrefix(plainKey),
		Name:        req.Name,
		Description: req.Description,
		Permissions: string(permsJSON),
		IPAddress:   req.IPAddress,
		ExpiresAt:   expiresAt,
	}

	if err := Db.Create(apiKey).Error; err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка сохранения ключа: "+err.Error(), nil)
		return
	}

	// ВОЗВРАЩАЕМ КЛЮЧ ТОЛЬКО ОДИН РАЗ
	response := CreateAPIKeyResponse{
		Key:         plainKey,
		Name:        apiKey.Name,
		Permissions: req.Permissions,
		IPAddress:   apiKey.IPAddress,
		ExpiresAt:   apiKey.ExpiresAt,
		CreatedAt:   apiKey.CreatedAt,
	}

	log.Printf("Создан API-ключ: ID=%d, Name=%s", apiKey.ID, apiKey.Name)

	sendResponse(c, http.StatusCreated, true, "API-ключ создан", response)
}

func HandleListAPIKeys(c *gin.Context) {
	var keys []APIKey
	if err := Db.Select("id, name, description, permissions, ip_address, expires_at, last_used_at, created_at").
		Order("created_at DESC").
		Find(&keys).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Ошибка получения ключей",
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"keys": keys},
	})
}

func HandleRevokeAPIKey(c *gin.Context) {
	keyID := c.Param("id")

	if currentKeyID, exists := c.Get("api_key_id"); exists {
		if fmt.Sprintf("%v", currentKeyID) == keyID {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"message": "Нельзя отозвать текущий ключ",
			})
			return
		}
	}

	if err := Db.Delete(&APIKey{}, keyID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Ошибка отзыва ключа",
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "API-ключ отозван",
	})
}

// HandleSyncZoneRecords возвращает все A записи зоны
func HandleSyncZoneRecords(c *gin.Context) {
	zoneName := c.Param("zoneName")

	zone, exists := getZoneFromConfig(zoneName)
	if !exists {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена", nil)
		return
	}

	records, err := readZoneFileSimple(zone.File)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка чтения зоны", err.Error())
		return
	}

	// Фильтруем только A и AAAA записи
	var aRecords []RecordInfo
	for _, rec := range records {
		if rec.Type == "A" || rec.Type == "AAAA" {
			aRecords = append(aRecords, rec)
		}
	}

	sendResponse(c, http.StatusOK, true, "A записи зоны", gin.H{
		"records": aRecords,
	})
}
