package internal

import (
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/gin-gonic/gin"
)

// --- Handlers ---

func HandleStatus(c *gin.Context) {
	cmd := exec.Command("systemctl", "is-active", "named")
	out, err := cmd.CombinedOutput()

	status := "inactive"
	if err == nil && strings.TrimSpace(string(out)) == "active" {
		status = "active"
	}

	response := gin.H{
		"named_status": status,
		"api_version":  "1.0.0",
		"role":         AppRole,
	}

	if AppRole == "master" {
		sqlDB, _ := Db.DB()
		response["db_connected"] = sqlDB.Ping() == nil
		response["queue_size"] = len(JQ)
	} else {
		response["master_url"] = os.Getenv("MASTER_URL")
		if RS != nil {
			response["last_sync"] = RS.GetLastSyncTime()
			response["sync_enabled"] = RS.Enabled
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
	var total, completed, failed int64

	Db.Model(&AuditLog{}).Count(&total)
	Db.Model(&AuditLog{}).Where("status = ?", "COMPLETED").Count(&completed)
	Db.Model(&AuditLog{}).Where("status = ?", "FAILED").Count(&failed)

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
