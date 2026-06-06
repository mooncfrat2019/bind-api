package internal

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

func getFileLock(filePath string) *sync.Mutex {
	FileLocksMutex.Lock()
	defer FileLocksMutex.Unlock()

	if _, exists := FileLocks[filePath]; !exists {
		FileLocks[filePath] = &sync.Mutex{}
	}
	return FileLocks[filePath]
}

func withFileLock(filePath string, fn func() error) error {
	lock := getFileLock(filePath)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

func sendResponse(c *gin.Context, status int, success bool, message string, data interface{}) {
	c.JSON(status, Response{
		Success: success,
		Message: message,
		Data:    data,
	})
}

func reloadBind() error {
	Debug("Выполнение rndc reload...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rndc", "reload")
	out, err := cmd.CombinedOutput()
	if err != nil {
		Error("rndc reload output: %s, error: %v", string(out), err)
		return fmt.Errorf("rndc reload failed: %v, output: %s", err, string(out))
	}
	Info("rndc reload выполнен успешно")
	return nil
}

func fixPermissions(filename string) error {
	// Проверка пути перед использованием
	filename = filepath.Clean(filename)
	if !strings.HasPrefix(filename, "/var/named/") &&
		!strings.HasPrefix(filename, "/etc/named") {
		Warn("fixPermissions отклонён для подозрительного пути: %s", filename)
		return fmt.Errorf("недопустимый путь файла")
	}

	// Используем CommandContext с таймаутом 10 секунд
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "chown", "named:named", filename)
	if err := cmd.Run(); err != nil {
		Error("chown failed for %s: %v", filename, err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	cmd = exec.CommandContext(ctx2, "chmod", "644", filename)
	if err := cmd.Run(); err != nil {
		Error("chmod failed for %s: %v", filename, err)
	}

	ctx3, cancel3 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel3()

	cmd = exec.CommandContext(ctx3, "restorecon", "-v", filename)
	_ = cmd.Run()
	return nil
}

// validateZoneName проверяет корректность имени зоны согласно RFC 1035
func validateZoneName(name string) bool {
	// Проверка на пустое имя
	if name == "" {
		return false
	}

	// Проверка на опасные символы для Command Injection
	dangerousChars := []string{
		";", "|", "&", "$", "`", "\\", "!", "'", "\"",
		"(", ")", "{", "}", "[", "]", "<", ">", "\n", "\r", "\t",
	}
	for _, char := range dangerousChars {
		if strings.Contains(name, char) {
			return false
		}
	}

	// Базовые проверки на опасные символы
	if strings.Contains(name, "..") || strings.Contains(name, "/") {
		return false
	}

	// Проверка что имя не начинается с дефиса или точки
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, ".") {
		return false
	}

	// Проверка длины
	if len(name) < 1 || len(name) > 253 {
		return false
	}
	// Проверка корректности меток (частей между точками)
	labels := strings.Split(name, ".")
	for _, label := range labels {
		// Каждая метка должна быть длиной от 1 до 63 символов
		if len(label) < 1 || len(label) > 63 {
			return false
		}

		// Метка должна содержать только допустимые символы
		for i, ch := range label {
			// Буквы, цифры и дефис разрешены
			isValid := (ch >= 'a' && ch <= 'z') ||
				(ch >= 'A' && ch <= 'Z') ||
				(ch >= '0' && ch <= '9') ||
				ch == '-'

			if !isValid {
				return false
			}

			// Дефис не может быть в начале или конце метки
			if ch == '-' && (i == 0 || i == len(label)-1) {
				return false
			}
		}
	}

	return true
}

// validateRecordName проверяет корректность имени записи
func validateRecordName(name string) bool {
	// Базовые проверки на опасные символы
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, ";") {
		return false
	}

	// Специальные разрешенные имена
	if name == "@" || name == "*" {
		return true
	}

	// Проверка длины (максимум 253 символа для полного имени)
	if len(name) > 253 {
		return false
	}

	// Проверка корректности меток
	labels := strings.Split(name, ".")
	for _, label := range labels {
		// Пустые метки не допускаются (например, "test..example")
		if label == "" {
			return false
		}

		// Каждая метка должна быть длиной от 1 до 63 символов
		if len(label) > 63 {
			return false
		}

		// Валидация символов в метке
		for j, ch := range label {
			// Разрешенные символы: буквы, цифры, дефис, подчеркивание (для служебных записей)
			isValid := (ch >= 'a' && ch <= 'z') ||
				(ch >= 'A' && ch <= 'Z') ||
				(ch >= '0' && ch <= '9') ||
				ch == '-' ||
				ch == '_'

			if !isValid {
				return false
			}

			// Дефис не может быть в начале или конце метки
			if ch == '-' && (j == 0 || j == len(label)-1) {
				return false
			}
		}
	}

	return true
}

// validateRecordValue проверяет значение записи в зависимости от типа
func validateRecordValue(recordType, value string) error {
	recordType = strings.ToUpper(recordType)

	switch recordType {
	case "A":
		// IPv4 адрес
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("неверный IPv4 адрес: %s", value)
		}
		return nil

	case "AAAA":
		// IPv6 адрес
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() != nil {
			return fmt.Errorf("неверный IPv6 адрес: %s", value)
		}
		return nil

	case "CNAME":
		// Canonical name - должно быть валидным доменным именем
		if !strings.HasSuffix(value, ".") {
			value = value + "."
		}
		if !validateRecordName(strings.TrimSuffix(value, ".")) {
			return fmt.Errorf("неверное имя для CNAME записи: %s", value)
		}
		return nil

	case "MX":
		// MX запись: priority hostname
		parts := strings.Fields(value)
		if len(parts) != 2 {
			return fmt.Errorf("MX запись должна содержать приоритет и имя хоста: %s", value)
		}

		// Проверка приоритета
		priority, err := strconv.Atoi(parts[0])
		if err != nil || priority < 0 || priority > 65535 {
			return fmt.Errorf("неверный приоритет MX записи: %s (должен быть 0-65535)", parts[0])
		}

		// Проверка имени хоста
		hostname := parts[1]
		if !strings.HasSuffix(hostname, ".") {
			hostname = hostname + "."
		}
		if !validateRecordName(strings.TrimSuffix(hostname, ".")) {
			return fmt.Errorf("неверное имя хоста в MX записи: %s", parts[1])
		}
		return nil

	case "TXT":
		// TXT запись - любая строка до 255 символов
		if len(value) > 255 {
			return fmt.Errorf("TXT запись слишком длинная: %d символов (максимум 255)", len(value))
		}
		return nil

	case "NS":
		// NS запись - должно быть валидным доменным именем
		if !strings.HasSuffix(value, ".") {
			value = value + "."
		}
		if !validateRecordName(strings.TrimSuffix(value, ".")) {
			return fmt.Errorf("неверное имя для NS записи: %s", value)
		}
		return nil

	case "PTR":
		// PTR запись - должно быть валидным доменным именем
		if !strings.HasSuffix(value, ".") {
			value = value + "."
		}
		if !validateRecordName(strings.TrimSuffix(value, ".")) {
			return fmt.Errorf("неверное имя для PTR записи: %s", value)
		}
		return nil

	case "SOA":
		// SOA запись имеет специальный формат, базовая проверка
		if len(value) < 10 {
			return fmt.Errorf("SOA запись слишком короткая")
		}
		return nil

	default:
		return fmt.Errorf("неподдерживаемый тип записи: %s", recordType)
	}
}

// validateTTL проверяет корректность TTL значения
func validateTTL(ttl int) error {
	const (
		MinTTL = 30
		MaxTTL = 604800
	)

	if ttl < MinTTL {
		return fmt.Errorf("TTL слишком мал: %d (минимум %d)", ttl, MinTTL)
	}
	if ttl > MaxTTL {
		return fmt.Errorf("TTL слишком велик: %d (максимум %d)", ttl, MaxTTL)
	}
	return nil
}

// validateDuplicateRecord проверяет наличие дубликата записи в зоне
func validateDuplicateRecord(zoneFile, recordName, recordType, recordValue string) error {
	records, err := readZoneFileSimple(zoneFile)
	if err != nil {
		return nil // Если не удалось прочитать, пропускаем проверку
	}

	for _, rec := range records {
		if rec.Name == recordName && rec.Type == recordType {
			// Для A/AAAA записей проверяем также значение
			if (recordType == "A" || recordType == "AAAA") && rec.Value == recordValue {
				return fmt.Errorf("запись %s типа %s со значением %s уже существует", recordName, recordType, recordValue)
			}
			// Для других типов просто наличие записи с таким именем и типом - уже дубликат
			if recordType != "A" && recordType != "AAAA" {
				return fmt.Errorf("запись %s типа %s уже существует", recordName, recordType)
			}
		}
	}

	return nil
}

// validateReverseZoneName проверяет корректность имени обратной зоны
func validateReverseZoneName(zoneName string) bool {
	// Проверка для IPv4 reverse zone (in-addr.arpa)
	if strings.HasSuffix(zoneName, ".in-addr.arpa") {
		prefix := strings.TrimSuffix(zoneName, ".in-addr.arpa")
		parts := strings.Split(prefix, ".")

		// Должно быть от 1 до 3 октетов
		if len(parts) < 1 || len(parts) > 3 {
			return false
		}

		// Каждый октет должен быть числом 0-255
		for _, part := range parts {
			num, err := strconv.Atoi(part)
			if err != nil || num < 0 || num > 255 {
				return false
			}
		}
		return true
	}

	// Проверка для IPv6 reverse zone (ip6.arpa)
	if strings.HasSuffix(zoneName, ".ip6.arpa") {
		prefix := strings.TrimSuffix(zoneName, ".ip6.arpa")
		// Упрощенная проверка: каждый символ должен быть hex цифрой или точкой
		for _, ch := range prefix {
			if ch != '.' && !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
				return false
			}
		}
		return true
	}

	// Обычная проверка для forward зоны
	return validateZoneName(zoneName)
}

// validateFilePath проверяет что путь к файлу безопасен
func validateFilePath(filePath, allowedDir string) bool {
	// Очистка пути
	filePath = filepath.Clean(filePath)
	allowedDir = filepath.Clean(allowedDir)

	// Проверка что путь начинается с разрешённой директории
	if !strings.HasPrefix(filePath, allowedDir) {
		return false
	}

	// Проверка на опасные символы
	dangerousChars := []string{";", "|", "&", "$", "`", "!", "'", "\""}
	for _, char := range dangerousChars {
		if strings.Contains(filePath, char) {
			return false
		}
	}

	// Проверка что путь не содержит ..
	if strings.Contains(filePath, "..") {
		return false
	}

	return true
}

func incrementSerial(zoneFile string) error {
	Debug("Увеличение Serial в файле: %s", zoneFile)

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
		Warn("Serial не был увеличен (не найден в файле)")
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
	Debug("Добавление записи в файл %s: %s", zoneFile, recordLine)
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

func getServerIPs() []string {
	var ips []string

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		Error("Не удалось получить IP адреса: %v", err)
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

// isIPv4 проверяет, является ли строка корректным IPv4-адресом
func IsIPv4(ip string) bool {
	octets := strings.Split(ip, ".")
	if len(octets) != 4 {
		return false
	}
	for _, octet := range octets {
		if len(octet) == 0 || len(octet) > 3 {
			return false
		}
		for _, ch := range octet {
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}
	return true
}

// checkBindStatus проверяет статус BIND в различных окружениях
func checkBindStatus() string {
	// Способ 1: Проверка через systemctl (для систем с systemd)
	if _, err := os.Stat("/usr/bin/systemctl"); err == nil {
		cmd := exec.Command("systemctl", "is-active", "named")
		out, err := cmd.CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) == "active" {
			return "active"
		}
	}

	// Способ 2: Проверка через service (для старых систем)
	if _, err := os.Stat("/usr/sbin/service"); err == nil {
		cmd := exec.Command("service", "named", "status")
		if err := cmd.Run(); err == nil {
			return "active"
		}
	}

	// Способ 3: Проверка через pgrep (для Docker контейнеров)
	cmd := exec.Command("pgrep", "-x", "named")
	if err := cmd.Run(); err == nil {
		return "active"
	}

	// Способ 4: Проверка через pid файл
	if _, err := os.Stat("/var/run/named/named.pid"); err == nil {
		pidData, err := os.ReadFile("/var/run/named/named.pid")
		if err == nil {
			pid := strings.TrimSpace(string(pidData))
			if pid != "" {
				// Проверяем, существует ли процесс с таким PID
				cmd := exec.Command("ps", "-p", pid)
				if err := cmd.Run(); err == nil {
					return "active"
				}
			}
		}
	}

	// Способ 5: Проверка через rndc (если он настроен)
	cmd2 := exec.Command("rndc", "status")
	out, err := cmd2.CombinedOutput()
	if err == nil && strings.Contains(string(out), "running") {
		return "active"
	}

	// Способ 6: Проверка через DNS запрос (если BIND слушает порт 53)
	conn, err := net.DialTimeout("udp", "127.0.0.1:53", 2*time.Second)
	if err == nil {
		conn.Close()
		return "active"
	}

	return "inactive"
}

// getEnvironment определяет окружение (Docker, systemd, etc)
func getEnvironment() string {
	// Проверка на Docker
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}

	// Проверка на Kubernetes
	if _, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount"); err == nil {
		return "kubernetes"
	}

	// Проверка на systemd
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return "systemd"
	}

	return "unknown"
}

// getBindProcessInfo возвращает информацию о процессе BIND
func getBindProcessInfo() map[string]interface{} {
	info := make(map[string]interface{})

	// Получаем PID named
	pid := getNamedPID()
	if pid == 0 {
		return nil
	}

	info["pid"] = pid

	// Получаем информацию о процессе
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "pcpu=% cpu%, pmem=% mem%, rss=% rss, vsz=% vsz, etime=% time")
	out, err := cmd.CombinedOutput()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		if len(lines) > 1 {
			fields := strings.Fields(lines[1])
			if len(fields) >= 5 {
				info["cpu"] = fields[0]
				info["memory"] = fields[1]
				info["rss"] = fields[2]
				info["vsz"] = fields[3]
				info["uptime"] = fields[4]
			}
		}
	}

	// Проверяем, слушает ли BIND порт 53
	info["listening_port_53"] = isPortListening(53)
	info["listening_port_953"] = isPortListening(953) // rndc port

	// Получаем количество зон
	if zones, err := parseZoneConfig(); err == nil {
		info["zones_loaded"] = len(zones)
	}

	return info
}

// getNamedPID возвращает PID процесса named
func getNamedPID() int {
	// Проверка через pid файл
	if _, err := os.Stat("/var/run/named/named.pid"); err == nil {
		pidData, err := os.ReadFile("/var/run/named/named.pid")
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if err == nil {
				return pid
			}
		}
	}

	// Проверка через pgrep
	cmd := exec.Command("pgrep", "-x", "named")
	out, err := cmd.Output()
	if err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err == nil {
			return pid
		}
	}

	return 0
}

// isPortListening проверяет, слушает ли процесс указанный порт
func isPortListening(port int) bool {
	// Проверка TCP порта
	tcpAddr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", tcpAddr, 1*time.Second)
	if err == nil {
		conn.Close()
		return true
	}

	// Проверка UDP порта (для DNS)
	if port == 53 {
		conn, err := net.DialTimeout("udp", tcpAddr, 1*time.Second)
		if err == nil {
			conn.Close()
			return true
		}
	}

	return false
}

// WaitForBind ожидает запуска BIND с таймаутом
func WaitForBind(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		if checkBindStatus() == "active" {
			Info("BIND successfully started")
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for BIND to start after %v", timeout)
		}

		<-ticker.C
	}
}

// StartBindIfNotRunning запускает BIND если он не запущен
func StartBindIfNotRunning() error {
	if checkBindStatus() == "active" {
		Info("BIND is already running")
		return nil
	}

	Info("Starting BIND...")

	// Пробуем запустить через systemctl
	cmd := exec.Command("systemctl", "start", "named")
	if err := cmd.Run(); err == nil {
		return WaitForBind(10 * time.Second)
	}

	// Пробуем запустить через service
	cmd = exec.Command("service", "named", "start")
	if err := cmd.Run(); err == nil {
		return WaitForBind(10 * time.Second)
	}

	// Пробуем запустить напрямую
	cmd = exec.Command("named", "-g", "-u", "named")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start named: %v", err)
	}

	return WaitForBind(10 * time.Second)
}

func ValidateMasterURL(masterURL string) (string, error) {
	allowInsecureSync := strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_INSECURE_SYNC")), "true")
	trimmed := strings.TrimSpace(masterURL)

	if trimmed == "" {
		if !allowInsecureSync {
			return "", fmt.Errorf("MASTER_URL не указан: используйте https URL или установите ALLOW_INSECURE_SYNC=true и REPLICA_MASTER_IP")
		}

		masterIP := strings.TrimSpace(os.Getenv("REPLICA_MASTER_IP"))
		if masterIP == "" {
			return "", fmt.Errorf("MASTER_URL не указан и REPLICA_MASTER_IP пуст")
		}

		ip := net.ParseIP(masterIP)
		if ip == nil {
			return "", fmt.Errorf("REPLICA_MASTER_IP содержит некорректный IP адрес: %s", masterIP)
		}

		masterPort := strings.TrimSpace(os.Getenv("MASTER_API_PORT"))
		if masterPort == "" {
			masterPort = "8080"
		}

		portNum, err := strconv.Atoi(masterPort)
		if err != nil || portNum < 1 || portNum > 65535 {
			return "", fmt.Errorf("MASTER_API_PORT должен быть числом от 1 до 65535")
		}

		builtURL := fmt.Sprintf("http://%s:%d", masterIP, portNum)
		Warn("MASTER_URL не задан, используется небезопасный адрес синхронизации, собранный из REPLICA_MASTER_IP: %s", builtURL)
		return builtURL, nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("некорректный MASTER_URL: %v", err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("некорректный MASTER_URL: требуется полный URL со схемой и хостом")
	}

	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		if !allowInsecureSync {
			return "", fmt.Errorf("MASTER_URL с http запрещён: используйте https или установите ALLOW_INSECURE_SYNC=true")
		}
		Warn("используется небезопасный MASTER_URL по HTTP, так как ALLOW_INSECURE_SYNC=true")
	default:
		return "", fmt.Errorf("неподдерживаемая схема MASTER_URL: %s", parsed.Scheme)
	}

	return strings.TrimRight(parsed.String(), "/"), nil
}

// IsIPInSubnet проверяет, находится ли IP в указанной подсети
func IsIPInSubnet(ipStr, subnetStr string) bool {
	if subnetStr == "" {
		return true // Если подсеть не указана, разрешаем все IP
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	_, subnet, err := net.ParseCIDR(subnetStr)
	if err != nil {
		Error("Неверный формат подсети %s: %v", subnetStr, err)
		return false
	}

	return subnet.Contains(ip)
}

// IsIPBlocked проверяет заблокирован ли IP
func IsIPBlocked(ip string) bool {
	SyncAuthLock.mu.RLock()
	until, blocked := SyncAuthLock.blockedIPs[ip]

	if !blocked {
		SyncAuthLock.mu.RUnlock()
		return false
	}

	// Если заблокирован, проверяем время
	if time.Now().Before(until) {
		SyncAuthLock.mu.RUnlock()
		return true
	}

	// Время блокировки истекло - нужно удалить
	SyncAuthLock.mu.RUnlock()

	// Переключаемся на write lock для удаления
	SyncAuthLock.mu.Lock()
	defer SyncAuthLock.mu.Unlock()

	// Проверяем ещё раз (могли удалить пока мы переключались)
	until, blocked = SyncAuthLock.blockedIPs[ip]
	if blocked && time.Now().After(until) {
		delete(SyncAuthLock.blockedIPs, ip)
		delete(SyncAuthLock.failedAttempts, ip)
		Info("IP %s разблокирован", ip)
	}

	return false
}

// RecordFailedAttempt записывает неудачную попытку авторизации
func RecordFailedAttempt(ip string) {
	SyncAuthLock.mu.Lock()
	defer SyncAuthLock.mu.Unlock()

	now := time.Now()

	// Если уже заблокирован - не записываем
	if _, blocked := SyncAuthLock.blockedIPs[ip]; blocked {
		return
	}

	attempt, exists := SyncAuthLock.failedAttempts[ip]
	if !exists {
		attempt = &FailedAuthAttempt{
			IP:        ip,
			Timestamp: now,
			Count:     1,
		}
		SyncAuthLock.failedAttempts[ip] = attempt
	} else {
		// Сбрасываем счётчик если прошло больше 5 минут
		if now.Sub(attempt.Timestamp) > 5*time.Minute {
			attempt.Count = 1
			attempt.Timestamp = now
		} else {
			attempt.Count++
		}
	}

	// Блокируем после 5 неудачных попыток
	if attempt.Count >= 5 {
		SyncAuthLock.blockedIPs[ip] = now.Add(15 * time.Minute) // Блокировка на 15 минут
		Warn("IP %s заблокирован на 15 минут после %d неудачных попыток", ip, attempt.Count)
	}
}

// ClearOldAttempts очищает старые попытки авторизации
func ClearOldAttempts() {
	SyncAuthLock.mu.Lock()
	defer SyncAuthLock.mu.Unlock()

	now := time.Now()
	cleared := 0

	for ip, attempt := range SyncAuthLock.failedAttempts {
		if now.Sub(attempt.Timestamp) > 10*time.Minute {
			delete(SyncAuthLock.failedAttempts, ip)
			cleared++
		}
	}

	// Также очищаем истёкшие блокировки
	for ip, until := range SyncAuthLock.blockedIPs {
		if now.After(until) {
			delete(SyncAuthLock.blockedIPs, ip)
			delete(SyncAuthLock.failedAttempts, ip)
			cleared++
		}
	}

	if cleared > 0 {
		Info("Очищено %d старых попыток авторизации", cleared)
	}
}

// StartAuthAttemptCleaner запускает фоновую задачу очистки попыток авторизации
func StartAuthAttemptCleaner() {
	Info("Запуск очистки старых попыток авторизации (интервал: 5 мин)")

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		// Первая очистка через 1 минуту после старта
		time.Sleep(1 * time.Minute)
		ClearOldAttempts()

		for range ticker.C {
			ClearOldAttempts()
		}
	}()
}
