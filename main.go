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

// --- Глобальные переменные ---
var (
	ZoneDir      string
	ZoneConfFile string
	NamedConf    string
)

// --- Константы ---
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

// --- Структуры ---

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
	ConfigFile string `json:"config_file"` // Путь к конфигу для новой зоны
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
	ConfigFile string // В каком файле определена зона
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

// incrementSerial увеличивает Serial
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

// readZoneFileSimple читает файл зоны
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

// parseZoneConfig парсит ВСЕ конфиги и возвращает список зон с указанием файла конфига
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

				// ИСПРАВЛЕНИЕ: Правильная обработка путей
				if !filepath.IsAbs(zoneFile) {
					// Если путь относительный, проверяем несколько вариантов
					possiblePaths := []string{
						filepath.Join(ZoneDir, zoneFile),
						filepath.Join(filepath.Dir(configFile), zoneFile),
						zoneFile,
					}

					// Ищем существующий файл
					found := false
					for _, path := range possiblePaths {
						if _, err := os.Stat(path); err == nil {
							zoneFile = path
							found = true
							log.Printf("Найден файл зоны: %s", zoneFile)
							break
						}
					}

					// Если ни один файл не найден, используем первый вариант
					if !found {
						zoneFile = possiblePaths[0]
						log.Printf("Файл зоны не найден, используем путь по умолчанию: %s", zoneFile)
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

// getZoneFromConfig возвращает полную информацию о зоне включая файл конфига
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

// getZoneConfigFile возвращает путь к конфигу где определена зона
func getZoneConfigFile(zoneName string) (string, bool) {
	zone, exists := getZoneFromConfig(zoneName)
	if !exists {
		return "", false
	}
	return zone.ConfigFile, true
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

// removeZoneFromConfig удаляет блок зоны из конкретного файла конфигурации
func removeZoneFromConfig(configFile, zoneName string) error {
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		log.Printf("Файл конфига не существует: %s", configFile)
		return nil
	}

	// 1. Сохраняем оригинальные права и владельца
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

		// Проверяем начало блока зоны
		if !skip && strings.HasPrefix(trimmed, "zone") && strings.Contains(trimmed, fmt.Sprintf(`"%s"`, zoneName)) {
			log.Printf("Найдена зона %s на строке %d", zoneName, i+1)
			zoneFound = true
			skip = true
			braceCount += strings.Count(line, "{")
			braceCount -= strings.Count(line, "}")
			continue
		}

		// Если пропускаем блок зоны
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

	// 2. Записываем во временный файл
	tmpFile := configFile + ".tmp"
	newContent := strings.Join(newLines, "\n")
	newContent = strings.TrimSpace(newContent) + "\n"

	if err := os.WriteFile(tmpFile, []byte(newContent), origMode); err != nil {
		return fmt.Errorf("ошибка записи временного файла: %v", err)
	}

	// 3. Восстанавливаем владельца (chown)
	// Для этого нужно использовать syscall или exec
	cmd := exec.Command("chown", "--reference="+configFile, tmpFile)
	if err := cmd.Run(); err != nil {
		// Альтернативный вариант если --reference не поддерживается
		cmd = exec.Command("chown", "root:named", tmpFile)
		_ = cmd.Run()
		log.Printf("WARNING: Не удалось восстановить владельца, пробуем root:named")
	}

	// 4. Проверяем синтаксис ПЕРЕД заменой оригинала
	cmd = exec.Command("named-checkconf", tmpFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("синтаксическая ошибка в конфиге: %s", string(out))
	}

	// 5. Заменяем оригинальный файл
	if err := os.Rename(tmpFile, configFile); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("ошибка замены конфига: %v", err)
	}

	// 6. Ещё раз проверяем права после переименования
	cmd = exec.Command("chmod", fmt.Sprintf("%o", origMode), configFile)
	_ = cmd.Run()

	cmd = exec.Command("chown", "root:named", configFile)
	_ = cmd.Run()

	log.Printf("Зона %s удалена из конфига %s", zoneName, configFile)
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
		"zones":       zones,
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

	// Проверяем существует ли зона в любом конфиге
	if zoneExistsInConfig(req.Name) {
		sendResponse(c, http.StatusConflict, false, "Зона уже существует в конфигурации", nil)
		return
	}

	// Определяем в какой конфиг записывать новую зону
	// Приоритет: 1) указанный в запросе, 2) где есть другие зоны, 3) ZoneConfFile
	targetConfigFile := req.ConfigFile
	if targetConfigFile == "" {
		// Ищем где уже есть зоны
		zones, _ := parseZoneConfig()
		if len(zones) > 0 {
			// Используем конфиг первой найденной зоны
			targetConfigFile = zones[0].ConfigFile
			log.Printf("Используем существующий конфиг: %s", targetConfigFile)
		} else {
			// Если зон нет, используем NamedConf (основной конфиг)
			targetConfigFile = NamedConf
			log.Printf("Используем основной конфиг: %s", targetConfigFile)
		}
	}

	// Проверяем существование файла конфига
	if _, err := os.Stat(targetConfigFile); os.IsNotExist(err) {
		// Если файл не существует, создаём или используем ZoneConfFile
		if targetConfigFile == NamedConf {
			sendResponse(c, http.StatusBadRequest, false, fmt.Sprintf("Основной конфиг не существует: %s", targetConfigFile), nil)
			return
		}
		targetConfigFile = ZoneConfFile
	}

	var zoneFile string
	if zoneType == "reverse" {
		zoneFile = filepath.Join(ZoneDir, req.Name+".rev")
	} else {
		zoneFile = filepath.Join(ZoneDir, req.Name+".zone")
	}

	log.Printf("Создание зоны %s, файл: %s, конфиг: %s", req.Name, zoneFile, targetConfigFile)

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

	nsIP := req.NsIP
	if nsIP == "" {
		serverIPs := getServerIPs()
		if len(serverIPs) > 0 {
			nsIP = serverIPs[0]
		} else {
			nsIP = "127.0.0.1"
		}
	}

	// Добавляем A запись для ns1
	zoneContent += fmt.Sprintf("ns1\t%d\tIN\tA\t%s\n", DefaultTTL, nsIP)

	log.Printf("Содержимое зоны:\n%s", zoneContent)

	if err := os.WriteFile(zoneFile, []byte(zoneContent), 0644); err != nil {
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

	log.Printf("Добавление в конфиг %s:\n%s", targetConfigFile, zoneConfig)

	confFile, err := os.OpenFile(targetConfigFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка записи в конфиг", err.Error())
		return
	}
	defer confFile.Close()

	if _, err := confFile.WriteString(zoneConfig); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка записи в конфиг", err.Error())
		return
	}
	confFile.Close()

	// Восстанавливаем права на конфиг после записи
	cmd := exec.Command("chown", "root:named", targetConfigFile)
	_ = cmd.Run()
	cmd = exec.Command("chmod", "640", targetConfigFile)
	_ = cmd.Run()
	log.Printf("Восстановлены права на конфиг %s", targetConfigFile)

	log.Println("Проверка синтаксиса named.conf...")
	cmd = exec.Command("named-checkconf")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("named-checkconf failed: %s", string(out))
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка в конфигурации named", string(out))
		return
	}

	log.Println("Проверка синтаксиса зоны...")
	cmd = exec.Command("named-checkzone", req.Name, zoneFile)
	out, err = cmd.CombinedOutput()
	if err != nil {
		log.Printf("named-checkzone failed: %s", string(out))
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка в файле зоны", string(out))
		return
	}
	log.Printf("named-checkzone output: %s", string(out))

	if err := reloadBind(); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Зона создана, но reload failed", err.Error())
		return
	}

	sendResponse(c, http.StatusOK, true, fmt.Sprintf("Зона %s (%s) создана в %s", req.Name, zoneType, targetConfigFile), gin.H{
		"zone_file":   zoneFile,
		"config_file": targetConfigFile,
	})
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

	zone, exists := getZoneFromConfig(zoneName)
	if !exists {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена в конфигурации", nil)
		return
	}

	log.Printf("Удаление зоны %s: файл=%s, конфиг=%s", zoneName, zone.File, zone.ConfigFile)

	// 1. Проверяем существует ли файл зоны
	if _, err := os.Stat(zone.File); os.IsNotExist(err) {
		log.Printf("WARNING: Файл зоны не существует: %s", zone.File)

		// Пробуем альтернативные пути
		alternativePaths := []string{
			filepath.Join(ZoneDir, zoneName+".zone"),
			filepath.Join(ZoneDir, zoneName+".rev"),
			filepath.Join(ZoneDir, strings.ReplaceAll(zoneName, ".", "_")+".rev"),
			filepath.Join(ZoneDir, filepath.Base(zone.File)),
		}

		found := false
		for _, altPath := range alternativePaths {
			if _, err := os.Stat(altPath); err == nil {
				log.Printf("Найден альтернативный путь: %s", altPath)
				zone.File = altPath
				found = true
				break
			}
		}

		if !found {
			log.Printf("Файл зоны не найден ни по одному из путей")
			// Продолжаем удаление из конфига даже если файла нет
		}
	}

	// 2. Удаляем файл зоны (если существует)
	if _, err := os.Stat(zone.File); err == nil {
		if err := os.Remove(zone.File); err != nil {
			sendResponse(c, http.StatusInternalServerError, false, "Не удалось удалить файл зоны", err.Error())
			return
		}
		log.Printf("Файл зоны удалён: %s", zone.File)
	} else {
		log.Printf("Файл зоны не существует, пропускаем удаление файла")
	}

	// 3. Удаляем зону ТОЛЬКО из того конфига где она определена
	if err := removeZoneFromConfig(zone.ConfigFile, zoneName); err != nil {
		log.Printf("ERROR: Не удалось удалить зону из конфига %s: %v", zone.ConfigFile, err)
		sendResponse(c, http.StatusInternalServerError, false, "Не удалось удалить зону из конфигурации", err.Error())
		return
	}

	// 4. Проверяем итоговый синтаксис конфига
	log.Println("Финальная проверка синтаксиса named.conf...")
	cmd := exec.Command("named-checkconf")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("named-checkconf failed после удаления зоны: %s", string(out))
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка в конфигурации после удаления зоны", string(out))
		return
	}

	// 5. Перезагружаем BIND
	if err := reloadBind(); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Зона удалена, но reload failed", err.Error())
		return
	}

	_, errZoneStat := os.Stat(zone.File)

	sendResponse(c, http.StatusOK, true, fmt.Sprintf("Зона %s удалена из %s", zoneName, zone.ConfigFile), gin.H{
		"config_file":  zone.ConfigFile,
		"zone_file":    zone.File,
		"file_existed": errZoneStat == nil,
	})
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

	zone, exists := getZoneFromConfig(zoneName)
	if !exists {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена в конфигурации", nil)
		return
	}

	log.Printf("Файл зоны: %s, конфиг: %s", zone.File, zone.ConfigFile)

	ttl := req.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}

	recordLine := fmt.Sprintf("%s\t%d\tIN\t%s\t%s", req.Name, ttl, req.Type, req.Value)

	if err := appendRecordToFile(zone.File, recordLine); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка записи в файл зоны", err.Error())
		return
	}

	if err := incrementSerial(zone.File); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка обновления Serial", err.Error())
		return
	}

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

	if err := fixPermissions(zone.File); err != nil {
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

	zone, exists := getZoneFromConfig(zoneName)
	if !exists {
		sendResponse(c, http.StatusNotFound, false, "Зона не найдена в конфигурации", nil)
		return
	}

	zoneType := "forward"
	if strings.Contains(zoneName, "in-addr.arpa") || strings.Contains(zoneName, "ip6.arpa") {
		zoneType = "reverse"
	}

	if zoneType == "forward" && (strings.ToUpper(recordType) == "A" || strings.ToUpper(recordType) == "AAAA") {
		records, err := readZoneFileSimple(zone.File)
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

	if err := deleteRecordFromFile(zone.File, recordName, strings.ToUpper(recordType)); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка удаления записи", err.Error())
		return
	}

	if err := incrementSerial(zone.File); err != nil {
		sendResponse(c, http.StatusInternalServerError, false, "Ошибка обновления Serial", err.Error())
		return
	}

	if err := fixPermissions(zone.File); err != nil {
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
