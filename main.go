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
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	PORT         string
	ZoneDir      string
	ZoneConfFile string
)

// --- Константы ---
const (
	DefaultPort         = "9002"
	DefaultZoneDir      = "/var/named/"
	DefaultZoneConfFile = "/etc/named.conf"
	DefaultTTL          = 3600
	DefaultRefresh      = 3600
	DefaultRetry        = 600
	DefaultExpire       = 604800
	DefaultNegative     = 3600
)

// --- Структуры ---

type Response struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type ZoneRequest struct {
	Name  string `json:"name" binding:"required"`
	Email string `json:"email"`
}

type RecordRequest struct {
	Name  string `json:"name" binding:"required"`
	Type  string `json:"type" binding:"required"`
	Value string `json:"value" binding:"required"`
	TTL   int    `json:"ttl"`
}

type ZoneInfo struct {
	Name        string `json:"name"`
	File        string `json:"file"`
	RecordCount int    `json:"record_count"`
}

type RecordInfo struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	TTL   int    `json:"ttl"`
	Value string `json:"value"`
}

// --- Утилиты ---

func initConfig() {
	ZoneDir = os.Getenv("BIND_ZONE_DIR")
	if ZoneDir == "" {
		ZoneDir = DefaultZoneDir
	}

	ZoneConfFile = os.Getenv("BIND_ZONE_CONF")
	if ZoneConfFile == "" {
		ZoneConfFile = DefaultZoneConfFile
	}

	PORT = os.Getenv("PORT")
	if PORT == "" {
		PORT = DefaultPort
	}

	log.Printf("Конфигурация: ZoneDir=%s, ZoneConfFile=%s", ZoneDir, ZoneConfFile)
}

func sendResponse(c *gin.Context, status int, success bool, message string, data interface{}) {
	c.JSON(status, Response{
		Success: success,
		Message: message,
		Data:    data,
	})
}

func reloadBind() error {
	cmd := exec.Command("rndc", "reload")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rndc reload failed: %v, output: %s", err, string(out))
	}
	return nil
}

func fixPermissions(filename string) error {
	cmd := exec.Command("chown", "named:named", filename)
	if err := cmd.Run(); err != nil {
		return err
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

// readZoneFileSimple читает файл зоны как текст и парсит базовые записи
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

		// Пропускаем комментарии и пустые строки
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "$") {
			continue
		}

		// Простой парсинг: имя [ttl] [класс] тип значение
		// Пример: www 3600 IN A 192.168.1.1
		// Пример: @ IN SOA ns1.example.com. admin.example.com. (...)

		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}

		rec := RecordInfo{}
		idx := 0

		// Имя записи
		rec.Name = parts[idx]
		idx++

		// Пропускаем числовые значения (возможно TTL)
		for idx < len(parts) {
			if _, err := fmt.Sscanf(parts[idx], "%d", new(int)); err == nil {
				rec.TTL, _ = fmt.Sscanf(parts[idx], "%d", new(int))
				idx++
				continue
			}
			if parts[idx] == "IN" || parts[idx] == "CH" || parts[idx] == "HS" {
				idx++
				continue
			}
			break
		}

		// Тип записи
		if idx < len(parts) {
			rec.Type = strings.ToUpper(parts[idx])
			idx++
		} else {
			continue
		}

		// Значение (всё остальное)
		if idx < len(parts) {
			rec.Value = strings.Join(parts[idx:], " ")
		}

		// Добавляем только "полезные" записи (пропускаем сложные типа SOA в простом выводе)
		if rec.Type == "A" || rec.Type == "AAAA" || rec.Type == "CNAME" || rec.Type == "MX" || rec.Type == "NS" || rec.Type == "TXT" {
			records = append(records, rec)
		}
	}

	return records, scanner.Err()
}

// appendRecordToFile добавляет запись в конец файла зоны
func appendRecordToFile(zoneFile, recordLine string) error {
	f, err := os.OpenFile(zoneFile, os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%s\n", recordLine)
	return err
}

// deleteRecordFromFile удаляет запись из файла (по имени и типу)
func deleteRecordFromFile(zoneFile, recordName, recordType string) error {
	// Читаем все строки
	file, err := os.Open(zoneFile)
	if err != nil {
		return err
	}

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Пропускаем комментарии и директивы
		if trimmed == "" || strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "$") {
			lines = append(lines, line)
			continue
		}

		// Проверяем, совпадает ли запись
		// Простая эвристика: строка начинается с имени (или @) и содержит тип
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 {
			name := fields[0]
			// Ищем тип в полях
			foundType := false
			for _, f := range fields {
				if strings.ToUpper(f) == strings.ToUpper(recordType) {
					foundType = true
					break
				}
			}
			// Если имя и тип совпадают - пропускаем строку (удаляем)
			// dns.Fqdn добавляет точку, сравниваем с учётом этого
			fqdnName := recordName
			if !strings.HasSuffix(fqdnName, ".") && fqdnName != "@" {
				fqdnName += "."
			}
			if (name == recordName || name == fqdnName || (recordName == "@" && name == "")) && foundType {
				continue // удаляем эту строку
			}
		}
		lines = append(lines, line)
	}
	file.Close()

	if err := scanner.Err(); err != nil {
		return err
	}

	// Перезаписываем файл
	return os.WriteFile(zoneFile, []byte(strings.Join(lines, "\n")+"\n"), 0640)
}

// --- Handlers ---

func handleStatus(c *gin.Context) {
	cmd := exec.Command("systemctl", "is-active", "named")
	out, err := cmd.CombinedOutput()

	status := "inactive"
	if err == nil && strings.TrimSpace(string(out)) == "active" {
		status = "active"
	}

	sendResponse(c, http.StatusOK, true, "Статус сервиса", gin.H{
		"named_status": status,
		"api_version":  "1.0.0",
	})
}

func handleListZones(c *gin.Context) {
	cmd := exec.Command("grep", "-oP", `zone\s+"\K[^"]+`, ZoneConfFile)
	out, err := cmd.CombinedOutput()

	var zones []ZoneInfo
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, line := range lines {
			if line != "" {
				zoneFile := filepath.Join(ZoneDir, line+".zone")
				count := 0
				if recs, err := readZoneFileSimple(zoneFile); err == nil {
					count = len(recs)
				}
				zones = append(zones, ZoneInfo{
					Name:        line,
					File:        zoneFile,
					RecordCount: count,
				})
			}
		}
	}

	sendResponse(c, http.StatusOK, true, "Список зон", gin.H{"zones": zones})
}

func handleCreateZone(c *gin.Context) {
	var req ZoneRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		sendResponse(c, http.StatusBadRequest, false, "Ошибка валидации JSON", err.Error())
		return
	}

	if !validateZoneName(req.Name) {
		sendResponse(c, http.StatusBadRequest, false, "Недопустимое имя зоны", nil)
		return
	}

	if req.Email == "" {
		req.Email = "admin." + req.Name
	}

	zoneFile := filepath.Join(ZoneDir, req.Name+".zone")

	if _, err := os.Stat(zoneFile); err == nil {
		sendResponse(c, http.StatusConflict, false, "Зона уже существует", nil)
		return
	}

	// Формируем минимальный файл зоны как текст
	soaEmail := strings.Replace(req.Email, "@", ".", -1)
	if !strings.HasSuffix(soaEmail, ".") {
		soaEmail += "."
	}

	zoneContent := fmt.Sprintf(`$TTL %d
@	IN	SOA	ns1.%s. %s (
					%d	; Serial
					%d	; Refresh
					%d	; Retry
					%d	; Expire
					%d )	; Negative Cache TTL
;
@	IN	NS	ns1.%s.
`, DefaultTTL, req.Name, soaEmail, uint32(time.Now().Unix()), DefaultRefresh, DefaultRetry, DefaultExpire, DefaultNegative, req.Name)

	if err := os.WriteFile(zoneFile, []byte(zoneContent), 0640); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Не удалось создать файл зоны", err.Error())
		return
	}

	if err := fixPermissions(zoneFile); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка прав доступа", err.Error())
		return
	}

	zoneConfig := fmt.Sprintf(`
zone "%s" {
    type master;
    file "%s";
};
`, req.Name, zoneFile)

	confFile, err := os.OpenFile(ZoneConfFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка записи в конфиг named", err.Error())
		return
	}
	defer confFile.Close()

	if _, err := confFile.WriteString(zoneConfig); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка записи в конфиг named", err.Error())
		return
	}

	if err := reloadBind(); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Зона создана, но reload failed", err.Error())
		return
	}

	sendResponse(c, http.StatusOK, true, fmt.Sprintf("Зона %s создана", req.Name), nil)
}

func handleGetZone(c *gin.Context) {
	zoneName := c.Param("name")
	if !validateZoneName(zoneName) {
		sendResponse(c, http.StatusBadRequest, false, "Недопустимое имя зоны", nil)
		return
	}

	zoneFile := filepath.Join(ZoneDir, zoneName+".zone")
	records, err := readZoneFileSimple(zoneFile)
	if err != nil {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена или не читается", err.Error())
		return
	}

	sendResponse(c, http.StatusOK, true, "Информация о зоне", gin.H{
		"name":         zoneName,
		"file":         zoneFile,
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

	zoneFile := filepath.Join(ZoneDir, zoneName+".zone")

	if err := os.Remove(zoneFile); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Не удалось удалить файл зоны", err.Error())
		return
	}

	// Удаляем блок зоны из конфига
	content, err := os.ReadFile(ZoneConfFile)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка чтения конфига", err.Error())
		return
	}

	// Простое удаление блока zone "name" { ... };
	lines := strings.Split(string(content), "\n")
	var newLines []string
	skip := false
	for _, line := range lines {
		if strings.Contains(line, fmt.Sprintf(`zone "%s"`, zoneName)) {
			skip = true
			continue
		}
		if skip {
			if strings.Contains(line, "};") {
				skip = false
			}
			continue
		}
		newLines = append(newLines, line)
	}

	if err := os.WriteFile(ZoneConfFile, []byte(strings.Join(newLines, "\n")), 0640); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка обновления конфига", err.Error())
		return
	}

	if err := fixPermissions(ZoneConfFile); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка прав конфига", err.Error())
		return
	}

	if err := reloadBind(); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Зона удалена, но reload failed", err.Error())
		return
	}

	sendResponse(c, http.StatusOK, true, fmt.Sprintf("Зона %s удалена", zoneName), nil)
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

	if !validateRecordName(req.Name) {
		sendResponse(c, http.StatusBadRequest, false, "Недопустимое имя записи", nil)
		return
	}

	// Валидация типа записи
	req.Type = strings.ToUpper(req.Type)
	if req.Type != "A" && req.Type != "AAAA" && req.Type != "CNAME" && req.Type != "MX" && req.Type != "TXT" && req.Type != "NS" {
		sendResponse(c, http.StatusBadRequest, false, "Поддерживаются только A, AAAA, CNAME, MX, TXT, NS", nil)
		return
	}

	// Валидация IP для A/AAAA
	if req.Type == "A" {
		ip := net.ParseIP(req.Value)
		if ip == nil || ip.To4() == nil {
			sendResponse(c, http.StatusBadRequest, false, "Неверный IPv4 адрес", nil)
			return
		}
	}
	if req.Type == "AAAA" {
		ip := net.ParseIP(req.Value)
		if ip == nil || ip.To4() != nil {
			sendResponse(c, http.StatusBadRequest, false, "Неверный IPv6 адрес", nil)
			return
		}
	}

	zoneFile := filepath.Join(ZoneDir, zoneName+".zone")

	// Формируем строку записи
	// Формат: [name] [ttl] IN [type] [value]
	ttl := req.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}

	recordLine := fmt.Sprintf("%s\t%d\tIN\t%s\t%s", req.Name, ttl, req.Type, req.Value)

	if err := appendRecordToFile(zoneFile, recordLine); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка записи в файл зоны", err.Error())
		return
	}

	if err := fixPermissions(zoneFile); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка прав", err.Error())
		return
	}

	if err := reloadBind(); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Запись добавлена, но reload failed", err.Error())
		return
	}

	sendResponse(c, http.StatusOK, true, "Запись добавлена", nil)
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

	zoneFile := filepath.Join(ZoneDir, zoneName+".zone")

	if err := deleteRecordFromFile(zoneFile, recordName, strings.ToUpper(recordType)); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка удаления записи", err.Error())
		return
	}

	if err := fixPermissions(zoneFile); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка прав", err.Error())
		return
	}

	if err := reloadBind(); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Запись удалена, но reload failed", err.Error())
		return
	}

	sendResponse(c, http.StatusOK, true, "Запись удалена", nil)
}

func handleReload(c *gin.Context) {
	if err := reloadBind(); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка reload", err.Error())
		return
	}
	sendResponse(c, http.StatusOK, true, "BIND перезагружен", nil)
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
	initConfig()

	if os.Geteuid() != 0 {
		log.Println("WARNING: Сервис запущен не от root. Возможны ошибки записи в /etc и /var/named")
	}

	if _, err := exec.LookPath("rndc"); err != nil {
		log.Fatal("Утилита rndc не найдена в PATH. Установите bind-utils")
	}

	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(loggerMiddleware())
	r.Use(gin.Recovery())

	api := r.Group("/api")
	{
		api.GET("/status", handleStatus)
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

	port := ":" + PORT
	log.Printf("BIND Manager API запущен на %s", port)

	if err := r.Run(port); err != nil {
		log.Fatal(err)
	}
}
