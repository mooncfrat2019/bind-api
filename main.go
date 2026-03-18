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
	"time"

	"github.com/gin-gonic/gin"
)

// --- Глобальные переменные (настраиваются через ENV) ---
var (
	ZoneDir      string
	ZoneConfFile string
	NamedConf    string // Путь к основному named.conf
)

// --- Константы по умолчанию ---
const (
	DefaultZoneDir      = "/var/named/"
	DefaultZoneConfFile = "/etc/named.zones.conf"
	DefaultNamedConf    = "/etc/named.conf"
	DefaultTTL          = 3600
	DefaultRefresh      = 3600
	DefaultRetry        = 600
	DefaultExpire       = 604800
	DefaultNegative     = 3600
)

// --- Структуры данных ---

type Response struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type ZoneRequest struct {
	Name  string `json:"name" binding:"required"`
	Email string `json:"email"`
	Type  string `json:"type"`
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
	RecordCount int    `json:"record_count"`
}

type RecordInfo struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	TTL   int    `json:"ttl"`
	Value string `json:"value"`
}

type ZoneConfig struct {
	Name string
	File string
	Type string
}

// --- Инициализация и Утилиты ---

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

	// Убедимся, что путь заканчивается на /
	if !strings.HasSuffix(ZoneDir, "/") {
		ZoneDir += "/"
	}

	log.Printf("Конфигурация: ZoneDir=%s, ZoneConfFile=%s, NamedConf=%s", ZoneDir, ZoneConfFile, NamedConf)
}

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

// incrementSerial увеличивает значение Serial в SOA записи на 1
func incrementSerial(zoneFile string) error {
	log.Printf("Увеличение Serial в файле: %s", zoneFile)

	// Проверка существования файла
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

	return os.WriteFile(zoneFile, []byte(strings.Join(newLines, "\n")), 0640)
}

// readZoneFileSimple читает файл зоны как текст
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
	f, err := os.OpenFile(zoneFile, os.O_APPEND|os.O_WRONLY, 0640)
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

	return os.WriteFile(zoneFile, []byte(strings.Join(lines, "\n")+"\n"), 0640)
}

// --- Парсинг конфига ---

// parseZoneConfig парсит файлы конфигурации и возвращает список зон
func parseZoneConfig() ([]ZoneConfig, error) {
	var zones []ZoneConfig
	configFiles := []string{NamedConf, ZoneConfFile}

	// Регулярное выражение для поиска зон
	// zone "name" IN { ... file "filename"; ... };
	zoneRegex := regexp.MustCompile(`zone\s+"([^"]+)"\s+(?:IN\s+)?\{[^}]*file\s+"([^"]+)"`)

	for _, configFile := range configFiles {
		if _, err := os.Stat(configFile); os.IsNotExist(err) {
			continue
		}

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

				// Если путь относительный, добавляем ZoneDir
				if !filepath.IsAbs(zoneFile) {
					zoneFile = filepath.Join(ZoneDir, zoneFile)
				}

				zoneType := "forward"
				if strings.Contains(zoneName, "in-addr.arpa") || strings.Contains(zoneName, "ip6.arpa") {
					zoneType = "reverse"
				}

				zones = append(zones, ZoneConfig{
					Name: zoneName,
					File: zoneFile,
					Type: zoneType,
				})
				log.Printf("Найдена зона в конфиге: %s -> %s (%s)", zoneName, zoneFile, zoneType)
			}
		}
	}

	return zones, nil
}

// getZoneFileFromConfig ищет файл зоны по имени в конфигурации
func getZoneFileFromConfig(zoneName string) (string, bool) {
	zones, err := parseZoneConfig()
	if err != nil {
		log.Printf("Ошибка парсинга конфига: %v", err)
		return "", false
	}

	for _, zone := range zones {
		if zone.Name == zoneName {
			log.Printf("Найден файл зоны %s в конфиге: %s", zoneName, zone.File)
			return zone.File, true
		}
	}

	log.Printf("Зона %s не найдена в конфиге", zoneName)
	return "", false
}

// zoneExistsInConfig проверяет существует ли зона в конфигурации
func zoneExistsInConfig(zoneName string) bool {
	_, exists := getZoneFileFromConfig(zoneName)
	return exists
}

// --- Reverse DNS утилиты ---

// getReverseZoneName возвращает имя обратной зоны для IPv4
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
		// 10.69.13.100 -> 13.69.10.in-addr.arpa
		return fmt.Sprintf("%s.%s.%s.in-addr.arpa", parts[2], parts[1], parts[0]), nil
	}

	return "", fmt.Errorf("IPv6 обратные зоны требуют ручной настройки")
}

// getPtrRecordName возвращает имя для PTR записи (последний октет для IPv4)
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

// addPtrRecord добавляет PTR запись в существующую обратную зону
func addPtrRecord(ip string, ptrName string, ttl int) error {
	log.Printf("Добавление PTR записи: IP=%s, PTR=%s", ip, ptrName)

	reverseZoneName, err := getReverseZoneName(ip)
	if err != nil {
		return fmt.Errorf("ошибка получения имени обратной зоны: %v", err)
	}
	log.Printf("Имя обратной зоны: %s", reverseZoneName)

	// Проверяем существует ли обратная зона в конфиге
	zoneFile, exists := getZoneFileFromConfig(reverseZoneName)
	if !exists {
		return fmt.Errorf("обратная зона %s не найдена в конфигурации", reverseZoneName)
	}

	log.Printf("Файл обратной зоны из конфига: %s", zoneFile)

	// Проверка существования файла
	if _, err := os.Stat(zoneFile); os.IsNotExist(err) {
		return fmt.Errorf("файл обратной зоны не существует: %s", zoneFile)
	}

	ptrRecordName, err := getPtrRecordName(ip)
	if err != nil {
		return fmt.Errorf("ошибка получения имени PTR записи: %v", err)
	}
	log.Printf("Имя PTR записи (октет): %s", ptrRecordName)

	if ttl == 0 {
		ttl = DefaultTTL
	}

	// Формат PTR записи: <октет> IN PTR <имя.домена.>
	recordLine := fmt.Sprintf("%s\t%d\tIN\tPTR\t%s", ptrRecordName, ttl, ptrName)
	log.Printf("Строка PTR записи: %s", recordLine)

	if err := appendRecordToFile(zoneFile, recordLine); err != nil {
		return fmt.Errorf("ошибка добавления записи в файл: %v", err)
	}

	if err := incrementSerial(zoneFile); err != nil {
		return fmt.Errorf("ошибка обновления Serial: %v", err)
	}

	if err := fixPermissions(zoneFile); err != nil {
		return err
	}

	log.Printf("PTR запись добавлена успешно")
	return nil
}

// deletePtrRecord удаляет PTR запись из обратной зоны
func deletePtrRecord(ip string) error {
	log.Printf("Удаление PTR записи для IP: %s", ip)

	reverseZoneName, err := getReverseZoneName(ip)
	if err != nil {
		return err
	}

	zoneFile, exists := getZoneFileFromConfig(reverseZoneName)
	if !exists {
		log.Printf("Обратная зона %s не найдена в конфиге, пропускаем удаление PTR", reverseZoneName)
		return nil
	}

	// Проверка существования файла
	if _, err := os.Stat(zoneFile); os.IsNotExist(err) {
		log.Printf("Файл обратной зоны не существует: %s", zoneFile)
		return nil
	}

	ptrRecordName, err := getPtrRecordName(ip)
	if err != nil {
		return err
	}

	if err := deleteRecordFromFile(zoneFile, ptrRecordName, "PTR"); err != nil {
		return err
	}

	if err := incrementSerial(zoneFile); err != nil {
		return err
	}

	if err := fixPermissions(zoneFile); err != nil {
		return err
	}

	log.Printf("PTR запись удалена успешно")
	return nil
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

func handleConfig(c *gin.Context) {
	zones, _ := parseZoneConfig()

	sendResponse(c, http.StatusOK, true, "Текущая конфигурация", gin.H{
		"zone_dir":    ZoneDir,
		"zone_conf":   ZoneConfFile,
		"named_conf":  NamedConf,
		"default_ttl": DefaultTTL,
		"api_port":    os.Getenv("API_PORT"),
		"gin_mode":    gin.Mode(),
		"running_as":  os.Geteuid(),
		"go_version":  strings.Replace(runtime.Version(), "go", "", -1),
		"zones_found": len(zones),
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

	if !validateZoneName(req.Name) {
		sendResponse(c, http.StatusBadRequest, false, "Недопустимое имя зоны", nil)
		return
	}

	if req.Email == "" {
		req.Email = "admin." + req.Name
	}

	zoneType := "forward"
	if req.Type == "reverse" || strings.Contains(req.Name, "in-addr.arpa") || strings.Contains(req.Name, "ip6.arpa") {
		zoneType = "reverse"
	}

	// Проверяем существует ли зона в конфиге
	if zoneExistsInConfig(req.Name) {
		sendResponse(c, http.StatusConflict, false, "Зона уже существует в конфигурации", nil)
		return
	}

	// Определяем имя файла
	var zoneFile string
	if zoneType == "reverse" {
		// Для обратной зоны используем .rev расширение
		zoneFile = filepath.Join(ZoneDir, req.Name+".rev")
		// Но в конфиге можно указать короткое имя
		zoneFile = filepath.Join(ZoneDir, strings.ReplaceAll(req.Name, ".in-addr.arpa", "")+".rev")
	} else {
		zoneFile = filepath.Join(ZoneDir, req.Name+".zone")
	}

	if _, err := os.Stat(zoneFile); err == nil {
		sendResponse(c, http.StatusConflict, false, "Файл зоны уже существует", nil)
		return
	}

	now := time.Now()
	serial := fmt.Sprintf("%d%02d%02d01", now.Year(), now.Month(), now.Day())

	soaEmail := strings.Replace(req.Email, "@", ".", -1)
	if !strings.HasSuffix(soaEmail, ".") {
		soaEmail += "."
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
`, DefaultTTL, req.Name, soaEmail, serial, DefaultRefresh, DefaultRetry, DefaultExpire, DefaultNegative, req.Name)

	if err := os.WriteFile(zoneFile, []byte(zoneContent), 0640); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Не удалось создать файл зоны", err.Error())
		return
	}

	if err := fixPermissions(zoneFile); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка прав доступа", err.Error())
		return
	}

	zoneConfig := fmt.Sprintf(`
zone "%s" IN {
    type master;
    file "%s";
    allow-update { none; };
};
`, req.Name, filepath.Base(zoneFile))

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

	sendResponse(c, http.StatusOK, true, fmt.Sprintf("Зона %s (%s) создана", req.Name, zoneType), nil)
}

func handleGetZone(c *gin.Context) {
	zoneName := c.Param("name")
	if !validateZoneName(zoneName) {
		sendResponse(c, http.StatusBadRequest, false, "Недопустимое имя зоны", nil)
		return
	}

	zoneFile, exists := getZoneFileFromConfig(zoneName)
	if !exists {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена в конфигурации", nil)
		return
	}

	zoneType := "forward"
	if strings.Contains(zoneName, "in-addr.arpa") || strings.Contains(zoneName, "ip6.arpa") {
		zoneType = "reverse"
	}

	records, err := readZoneFileSimple(zoneFile)
	if err != nil {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена или не читается", err.Error())
		return
	}

	sendResponse(c, http.StatusOK, true, "Информация о зоне", gin.H{
		"name":         zoneName,
		"type":         zoneType,
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

	zoneFile, exists := getZoneFileFromConfig(zoneName)
	if !exists {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена в конфигурации", nil)
		return
	}

	if err := os.Remove(zoneFile); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Не удалось удалить файл зоны", err.Error())
		return
	}

	// Удаляем блок зоны из всех конфигов
	configFiles := []string{NamedConf, ZoneConfFile}
	for _, configFile := range configFiles {
		if _, err := os.Stat(configFile); os.IsNotExist(err) {
			continue
		}

		content, err := os.ReadFile(configFile)
		if err != nil {
			continue
		}

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

		if err := os.WriteFile(configFile, []byte(strings.Join(newLines, "\n")), 0640); err != nil {
			log.Printf("WARNING: Не удалось обновить конфиг %s: %v", configFile, err)
		}
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

	req.Type = strings.ToUpper(req.Type)
	if req.Type != "A" && req.Type != "AAAA" && req.Type != "CNAME" && req.Type != "MX" && req.Type != "TXT" && req.Type != "NS" {
		sendResponse(c, http.StatusBadRequest, false, "Поддерживаются только A, AAAA, CNAME, MX, TXT, NS", nil)
		return
	}

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

	// Получаем файл зоны из конфига
	zoneFile, exists := getZoneFileFromConfig(zoneName)
	if !exists {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена в конфигурации", nil)
		return
	}

	log.Printf("Файл зоны из конфига: %s", zoneFile)

	ttl := req.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}

	recordLine := fmt.Sprintf("%s\t%d\tIN\t%s\t%s", req.Name, ttl, req.Type, req.Value)

	if err := appendRecordToFile(zoneFile, recordLine); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка записи в файл зоны", err.Error())
		return
	}

	if err := incrementSerial(zoneFile); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка обновления Serial", err.Error())
		return
	}

	// Автоматическое создание PTR записи для A/AAAA записей
	if (req.Type == "A" || req.Type == "AAAA") && req.ReversePtr != "" {
		ptrName := req.ReversePtr
		if !strings.HasSuffix(ptrName, ".") {
			ptrName += "."
		}

		log.Printf("Попытка создания PTR записи: %s -> %s", req.Value, ptrName)
		if err := addPtrRecord(req.Value, ptrName, ttl); err != nil {
			log.Printf("WARNING: Не удалось создать PTR запись: %v", err)
			sendResponse(c, http.StatusOK, true, "Запись добавлена (PTR не создана: "+err.Error()+")", nil)
			return
		}
		log.Printf("PTR запись создана успешно")
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

	zoneFile, exists := getZoneFileFromConfig(zoneName)
	if !exists {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена в конфигурации", nil)
		return
	}

	// Для A/AAAA записей удаляем соответствующую PTR запись
	zoneType := "forward"
	if strings.Contains(zoneName, "in-addr.arpa") || strings.Contains(zoneName, "ip6.arpa") {
		zoneType = "reverse"
	}

	if zoneType == "forward" && (strings.ToUpper(recordType) == "A" || strings.ToUpper(recordType) == "AAAA") {
		records, err := readZoneFileSimple(zoneFile)
		if err == nil {
			for _, rec := range records {
				if rec.Name == recordName && rec.Type == strings.ToUpper(recordType) {
					log.Printf("Найдена запись для удаления PTR: %s -> %s", rec.Name, rec.Value)
					if err := deletePtrRecord(rec.Value); err != nil {
						log.Printf("WARNING: Не удалось удалить PTR запись: %v", err)
					}
					break
				}
			}
		}
	}

	if err := deleteRecordFromFile(zoneFile, recordName, strings.ToUpper(recordType)); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка удаления записи", err.Error())
		return
	}

	if err := incrementSerial(zoneFile); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка обновления Serial", err.Error())
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
		port = ":9002"
	}

	log.Printf("BIND Manager API запущен на порту %s", port)
	log.Printf("Используемые пути: ZoneDir=%s, ZoneConfFile=%s, NamedConf=%s", ZoneDir, ZoneConfFile, NamedConf)

	if err := r.Run(port); err != nil {
		log.Fatal(err)
	}
}
