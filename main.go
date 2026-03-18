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
)

// --- Константы по умолчанию ---
const (
	DefaultZoneDir      = "/var/named/"
	DefaultZoneConfFile = "/etc/named.conf"
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
	Type  string `json:"type"` // "forward" или "reverse"
}

type RecordRequest struct {
	Name       string `json:"name" binding:"required"`
	Type       string `json:"type" binding:"required"`
	Value      string `json:"value" binding:"required"`
	TTL        int    `json:"ttl"`
	ReversePtr string `json:"reverse_ptr"` // Имя для PTR записи (опционально)
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

// incrementSerial увеличивает значение Serial в SOA записи на 1
func incrementSerial(zoneFile string) error {
	content, err := os.ReadFile(zoneFile)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string
	inSoa := false
	soaComplete := false

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

// --- Reverse DNS утилиты ---

// getReverseZoneName возвращает имя обратной зоны для IPv4
func getReverseZoneName(ip string) (string, error) {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return "", fmt.Errorf("неверный IP адрес")
	}

	if parsedIP.To4() != nil {
		// IPv4: 192.168.1.100 -> 1.168.192.in-addr.arpa
		parts := strings.Split(parsedIP.To4().String(), ".")
		if len(parts) != 4 {
			return "", fmt.Errorf("неверный формат IPv4")
		}
		return fmt.Sprintf("%s.%s.%s.in-addr.arpa", parts[2], parts[1], parts[0]), nil
	}

	// IPv6: упрощённая поддержка /64
	// 2001:db8::1 -> 8.b.d.0.1.0.0.2.ip6.arpa (первые 64 бита)
	return "", fmt.Errorf("IPv6 обратные зоны требуют ручной настройки")
}

// getReverseZoneFile возвращает путь к файлу обратной зоны
func getReverseZoneFile(reverseZoneName string) string {
	// Заменяем точки на подчёркивания для имени файла
	fileName := strings.ReplaceAll(reverseZoneName, ".", "_")
	return filepath.Join(ZoneDir, fileName+".rev")
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

// createReverseZone создаёт файл обратной зоны
func createReverseZone(reverseZoneName string, email string) error {
	zoneFile := getReverseZoneFile(reverseZoneName)

	// Проверка на существование
	if _, err := os.Stat(zoneFile); err == nil {
		return nil // Зона уже существует
	}

	now := time.Now()
	serial := fmt.Sprintf("%d%02d%02d01", now.Year(), now.Month(), now.Day())

	zoneContent := fmt.Sprintf(`$TTL %d
@	IN	SOA	ns1.%s. %s (
					%s	; Serial
					%d	; Refresh
					%d	; Retry
					%d	; Expire
					%d )	; Negative Cache TTL
;
@	IN	NS	ns1.%s.
`, DefaultTTL, reverseZoneName, email, serial, DefaultRefresh, DefaultRetry, DefaultExpire, DefaultNegative, reverseZoneName)

	if err := os.WriteFile(zoneFile, []byte(zoneContent), 0640); err != nil {
		return err
	}

	if err := fixPermissions(zoneFile); err != nil {
		return err
	}

	// Добавляем объявление зоны в конфиг
	zoneConfig := fmt.Sprintf(`
zone "%s" {
    type master;
    file "%s";
};
`, reverseZoneName, zoneFile)

	confFile, err := os.OpenFile(ZoneConfFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer confFile.Close()

	if _, err := confFile.WriteString(zoneConfig); err != nil {
		return err
	}

	return nil
}

// addPtrRecord добавляет PTR запись в обратную зону
func addPtrRecord(ip string, ptrName string, ttl int) error {
	reverseZoneName, err := getReverseZoneName(ip)
	if err != nil {
		return err
	}

	// Создаём обратную зону если не существует
	if err := createReverseZone(reverseZoneName, "admin."+reverseZoneName); err != nil {
		return err
	}

	zoneFile := getReverseZoneFile(reverseZoneName)
	ptrRecordName, err := getPtrRecordName(ip)
	if err != nil {
		return err
	}

	if ttl == 0 {
		ttl = DefaultTTL
	}

	// Формат PTR записи: <октет> IN PTR <имя.домена.>
	recordLine := fmt.Sprintf("%s\t%d\tIN\tPTR\t%s", ptrRecordName, ttl, ptrName)

	if err := appendRecordToFile(zoneFile, recordLine); err != nil {
		return err
	}

	if err := incrementSerial(zoneFile); err != nil {
		return err
	}

	if err := fixPermissions(zoneFile); err != nil {
		return err
	}

	return nil
}

// deletePtrRecord удаляет PTR запись из обратной зоны
func deletePtrRecord(ip string) error {
	reverseZoneName, err := getReverseZoneName(ip)
	if err != nil {
		return err
	}

	zoneFile := getReverseZoneFile(reverseZoneName)

	// Проверка существования файла
	if _, err := os.Stat(zoneFile); os.IsNotExist(err) {
		return nil // Файл не существует, ничего не делаем
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
	sendResponse(c, http.StatusOK, true, "Текущая конфигурация", gin.H{
		"zone_dir":    ZoneDir,
		"zone_conf":   ZoneConfFile,
		"default_ttl": DefaultTTL,
		"api_port":    os.Getenv("PORT"),
		"gin_mode":    gin.Mode(),
		"running_as":  os.Geteuid(),
		"go_version":  strings.Replace(runtime.Version(), "go", "", -1),
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
				zoneType := "forward"
				if strings.Contains(line, "in-addr.arpa") || strings.Contains(line, "ip6.arpa") {
					zoneType = "reverse"
				}

				zoneFile := filepath.Join(ZoneDir, line+".zone")
				if zoneType == "reverse" {
					fileName := strings.ReplaceAll(line, ".", "_")
					zoneFile = filepath.Join(ZoneDir, fileName+".rev")
				}

				count := 0
				if recs, err := readZoneFileSimple(zoneFile); err == nil {
					count = len(recs)
				}
				zones = append(zones, ZoneInfo{
					Name:        line,
					File:        zoneFile,
					Type:        zoneType,
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

	// Определяем тип зоны
	zoneType := "forward"
	if req.Type == "reverse" || strings.Contains(req.Name, "in-addr.arpa") || strings.Contains(req.Name, "ip6.arpa") {
		zoneType = "reverse"
	}

	var zoneFile string
	if zoneType == "reverse" {
		fileName := strings.ReplaceAll(req.Name, ".", "_")
		zoneFile = filepath.Join(ZoneDir, fileName+".rev")
	} else {
		zoneFile = filepath.Join(ZoneDir, req.Name+".zone")
	}

	if _, err := os.Stat(zoneFile); err == nil {
		sendResponse(c, http.StatusConflict, false, "Зона уже существует", nil)
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

	sendResponse(c, http.StatusOK, true, fmt.Sprintf("Зона %s (%s) создана", req.Name, zoneType), nil)
}

func handleGetZone(c *gin.Context) {
	zoneName := c.Param("name")
	if !validateZoneName(zoneName) {
		sendResponse(c, http.StatusBadRequest, false, "Недопустимое имя зоны", nil)
		return
	}

	zoneType := "forward"
	if strings.Contains(zoneName, "in-addr.arpa") || strings.Contains(zoneName, "ip6.arpa") {
		zoneType = "reverse"
	}

	var zoneFile string
	if zoneType == "reverse" {
		fileName := strings.ReplaceAll(zoneName, ".", "_")
		zoneFile = filepath.Join(ZoneDir, fileName+".rev")
	} else {
		zoneFile = filepath.Join(ZoneDir, zoneName+".zone")
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

	zoneType := "forward"
	if strings.Contains(zoneName, "in-addr.arpa") || strings.Contains(zoneName, "ip6.arpa") {
		zoneType = "reverse"
	}

	var zoneFile string
	if zoneType == "reverse" {
		fileName := strings.ReplaceAll(zoneName, ".", "_")
		zoneFile = filepath.Join(ZoneDir, fileName+".rev")
	} else {
		zoneFile = filepath.Join(ZoneDir, zoneName+".zone")
	}

	if err := os.Remove(zoneFile); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Не удалось удалить файл зоны", err.Error())
		return
	}

	content, err := os.ReadFile(ZoneConfFile)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка чтения конфига", err.Error())
		return
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

	zoneType := "forward"
	if strings.Contains(zoneName, "in-addr.arpa") || strings.Contains(zoneName, "ip6.arpa") {
		zoneType = "reverse"
	}

	var zoneFile string
	if zoneType == "reverse" {
		fileName := strings.ReplaceAll(zoneName, ".", "_")
		zoneFile = filepath.Join(ZoneDir, fileName+".rev")
	} else {
		zoneFile = filepath.Join(ZoneDir, zoneName+".zone")
	}

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
		// Добавляем точку в конец если нет
		ptrName := req.ReversePtr
		if !strings.HasSuffix(ptrName, ".") {
			ptrName += "."
		}

		if err := addPtrRecord(req.Value, ptrName, ttl); err != nil {
			log.Printf("WARNING: Не удалось создать PTR запись: %v", err)
			// Не прерываем операцию, PTR опционален
		}
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

	zoneType := "forward"
	if strings.Contains(zoneName, "in-addr.arpa") || strings.Contains(zoneName, "ip6.arpa") {
		zoneType = "reverse"
	}

	var zoneFile string
	if zoneType == "reverse" {
		fileName := strings.ReplaceAll(zoneName, ".", "_")
		zoneFile = filepath.Join(ZoneDir, fileName+".rev")
	} else {
		zoneFile = filepath.Join(ZoneDir, zoneName+".zone")
	}

	// Для A/AAAA записей удаляем соответствующую PTR запись
	if zoneType == "forward" && (strings.ToUpper(recordType) == "A" || strings.ToUpper(recordType) == "AAAA") {
		// Читаем зону чтобы найти IP адрес
		records, err := readZoneFileSimple(zoneFile)
		if err == nil {
			for _, rec := range records {
				if rec.Name == recordName && rec.Type == strings.ToUpper(recordType) {
					// Нашли запись, удаляем PTR
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

	port := os.Getenv("PORT")
	if port == "" {
		port = ":9002"
	}

	log.Printf("BIND Manager API запущен на порту %s", port)
	log.Printf("Используемые пути: ZoneDir=%s, ZoneConfFile=%s", ZoneDir, ZoneConfFile)

	if err := r.Run(port); err != nil {
		log.Fatal(err)
	}
}
