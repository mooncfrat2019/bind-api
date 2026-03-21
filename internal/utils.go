package internal

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

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
