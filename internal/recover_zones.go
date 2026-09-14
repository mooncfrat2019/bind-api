package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReloadBind — экспортируемая обёртка над reloadBind(), чтобы main.go
// мог перезагрузить BIND после восстановления конфигурации.
func ReloadBind() error {
	return reloadBind()
}

// RebuildZonesConfFromDisk пересобирает named.zones.conf из файлов зон,
// лежащих в ZoneDir.
//
// Зачем это нужно:
//   - сами файлы зон лежат на постоянном томе (PVC, /var/named);
//   - файл named.zones.conf на мастере лежит на emptyDir и теряется
//     при пересоздании пода;
//   - имена файлов формируются детерминированно (<zone>.zone / <zone>.rev),
//     поэтому конфиг полностью восстанавливается из содержимого каталога зон.
//
// Вызывать один раз при старте мастера, после InitConfig.
func RebuildZonesConfFromDisk() error {
	if AppRole != "master" {
		return nil
	}

	zoneConfFile := ZoneConfFile
	if zoneConfFile == "" {
		zoneConfFile = DefaultZoneConfFile
	}

	zoneDir := ZoneDir
	if zoneDir == "" {
		zoneDir = DefaultZoneDir
	}

	entries, err := os.ReadDir(zoneDir)
	if err != nil {
		return fmt.Errorf("не удалось прочитать директорию зон %s: %w", zoneDir, err)
	}

	var sb strings.Builder
	sb.WriteString("// Файл автоматически восстановлен BIND-API из файлов зон на диске.\n")
	sb.WriteString("// Ручные правки будут перезаписаны при следующем старте.\n")

	restored := 0
	skipped := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()

		var zoneName string
		switch {
		case strings.HasSuffix(name, ".zone"):
			zoneName = strings.TrimSuffix(name, ".zone")
		case strings.HasSuffix(name, ".rev"):
			zoneName = strings.TrimSuffix(name, ".rev")
		default:
			continue
		}

		// Отсекаем временные файлы на всякий случай
		if strings.Contains(zoneName, ".tmp") || strings.Contains(zoneName, ".rollback") {
			continue
		}

		if !validateZoneName(zoneName) {
			skipped++
			Warn("Восстановление named.zones.conf: пропущен файл %s (некорректное имя зоны %q)", name, zoneName)
			continue
		}

		sb.WriteString(fmt.Sprintf(`
zone "%s" IN {
         type master;
         file "%s";
         allow-update { none; };
};
`, zoneName, name))
		restored++
	}

	content := sb.String()

	// Атомарная замена через временный файл в той же директории
	dir := filepath.Clean(filepath.Dir(zoneConfFile))
	tmpPath := filepath.Clean(zoneConfFile + ".rebuild.tmp")
	if !strings.HasPrefix(tmpPath, dir) {
		return fmt.Errorf("некорректный путь временного файла: %s", tmpPath)
	}

	if err := os.WriteFile(tmpPath, []byte(content), 0640); err != nil {
		return fmt.Errorf("не удалось записать временный named.zones.conf: %w", err)
	}

	if err := os.Rename(tmpPath, zoneConfFile); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("не удалось заменить named.zones.conf: %w", err)
	}

	if err := os.Chmod(zoneConfFile, 0640); err != nil {
		Warn("Не удалось выставить права 0640 на %s: %v", zoneConfFile, err)
	}

	Info("named.zones.conf восстановлен из файлов зон: восстановлено=%d, пропущено=%d, файл=%s",
		restored, skipped, zoneConfFile)

	return nil
}
