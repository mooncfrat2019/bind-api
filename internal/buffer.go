package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// InitAsyncBuffer инициализирует асинхронный буфер
func InitAsyncBuffer() {
	// Определяем путь к WAL файлу
	walPath := os.Getenv("BIND_API_WAL_PATH")

	if walPath == "" {
		// Если не задан, используем директорию с бинарным файлом
		execPath, err := os.Executable()
		if err != nil {
			Error("Не удалось получить путь к исполняемому файлу: %v", err)
			execPath = "."
		}
		execDir := filepath.Dir(execPath)
		walPath = filepath.Join(execDir, "logs", "bind-api-wal.log")
	}

	// Создаем директорию для WAL если её нет
	walDir := filepath.Dir(walPath)
	if err := os.MkdirAll(walDir, 0755); err != nil {
		Error("Не удалось создать директорию для WAL %s: %v", walDir, err)
		// Fallback к временной директории
		walPath = filepath.Join(os.TempDir(), "bind-api-wal.log")
		os.MkdirAll(filepath.Dir(walPath), 0755)
	}

	walFile, err := os.OpenFile(walPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		Error("Не удалось создать WAL файл %s: %v", walPath, err)
		// Продолжаем без WAL (асинхронная запись без гарантий)
		walFile = nil
	} else {
		Debug("WAL файл: %s", walPath)
	}

	RecordBuffer = &AsyncRecordBuffer{
		pending:   make(map[string][]string),
		flushCh:   make(chan struct{}, 1),
		batchSize: BatchSize,
		interval:  BatchInterval,
		walFile:   walFile,
		walPath:   walPath,
	}

	// Восстанавливаем из WAL при старте. Файл НЕ обрезаем — его содержимое
	// должно совпадать с b.pending, чтобы падение до первого flush
	// не привело к потере записей.
	if walFile != nil {
		RecordBuffer.recoverFromWAL()
	}

	go RecordBuffer.worker()
	Info("Асинхронный буфер инициализирован: batchSize=%d, interval=%v, wal=%s",
		BatchSize, BatchInterval, walPath)
}

// recoverFromWAL восстанавливает данные из WAL после рестарта.
// ВАЖНО: WAL не обрезается. Обрезка (точнее — перезапись ровно по pending)
// происходит только после успешного сброса в файлы зон, см. rewriteWAL.
func (b *AsyncRecordBuffer) recoverFromWAL() {
	if b.walFile == nil {
		return
	}

	// Гарантируем, что все ранее записанные данные уже на диске
	if err := b.walFile.Sync(); err != nil {
		Error("Не удалось синхронизировать WAL %s: %v", b.walPath, err)
	}

	content, err := os.ReadFile(b.walPath)
	if err != nil {
		Error("Не удалось прочитать WAL файл %s: %v", b.walPath, err)
		return
	}

	lines := strings.Split(string(content), "\n")
	recoveredCount := 0

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) == 2 {
			zoneName := parts[0]
			recordLine := parts[1]
			b.pending[zoneName] = append(b.pending[zoneName], recordLine)
			recoveredCount++
		}
	}

	if recoveredCount > 0 {
		Info("Восстановлено %d записей из WAL", recoveredCount)
		// Сигналим воркеру сбросить восстановленные записи как можно скорее.
		// Сам flush здесь не вызываем: воркер ещё не запущен.
		select {
		case b.flushCh <- struct{}{}:
		default:
		}
	}
}

// Add добавляет запись в буфер.
// WAL и pending изменяются под ОДНИМ мьютексом b.mu, чтобы они никогда
// не разъезжались между собой.
func (b *AsyncRecordBuffer) Add(zoneName, recordLine string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Пишем в WAL для надежности
	if b.walFile != nil {
		walEntry := fmt.Sprintf("%s|%s\n", zoneName, recordLine)
		if _, err := b.walFile.WriteString(walEntry); err != nil {
			Error("Ошибка записи в WAL: %v", err)
		}
	}

	b.pending[zoneName] = append(b.pending[zoneName], recordLine)
	batchSize := len(b.pending[zoneName])

	// Если накопилось достаточно - сигналим
	if batchSize >= b.batchSize {
		select {
		case b.flushCh <- struct{}{}:
		default:
		}
	}
}

func (b *AsyncRecordBuffer) worker() {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.flush()
		case <-b.flushCh:
			b.flush()
		}
	}
}

// flush сбрасывает накопленные записи в файлы зон.
// Неудачные записи возвращаются в b.pending для повторной попытки.
// После сброса WAL перезаписывается так, чтобы ровно соответствовать b.pending.
func (b *AsyncRecordBuffer) flush() {
	b.mu.Lock()
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	toFlush := b.pending
	b.pending = make(map[string][]string)
	b.mu.Unlock()

	// Параллельная запись по зонам
	var wg sync.WaitGroup
	var failedMu sync.Mutex
	failed := make(map[string][]string)

	for zoneName, records := range toFlush {
		wg.Add(1)
		go func(zoneName string, records []string) {
			defer wg.Done()
			if err := b.writeToZone(zoneName, records); err != nil {
				Error("async buffer flush error: %s: %v", zoneName, err)
				failedMu.Lock()
				failed[zoneName] = append(failed[zoneName], records...)
				failedMu.Unlock()
			}
		}(zoneName, records)
	}
	wg.Wait()

	// Возвращаем неудачные записи в pending, чтобы повторить их позже
	if len(failed) > 0 {
		b.mu.Lock()
		for zoneName, records := range failed {
			b.pending[zoneName] = append(b.pending[zoneName], records...)
		}
		b.mu.Unlock()
	}

	// WAL теперь должен содержать ТОЛЬКО то, что ещё не записано в зоны
	b.rewriteWAL()
}

// writeToZone записывает набор строк в файл зоны и увеличивает serial.
// Возвращает ошибку, чтобы flush мог вернуть запись в очередь на повтор.
func (b *AsyncRecordBuffer) writeToZone(zoneName string, records []string) error {
	zone, exists := getZoneFromConfig(zoneName)
	if !exists {
		return fmt.Errorf("зона %s не найдена в конфиге", zoneName)
	}

	err := withFileLock(zone.File, func() error {
		for _, record := range records {
			if errAppend := appendRecordToFile(zone.File, record); errAppend != nil {
				return fmt.Errorf("append record error: %w", errAppend)
			}
		}
		if errSerial := incrementSerial(zone.File); errSerial != nil {
			return fmt.Errorf("increment serial error: %w", errSerial)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if errPermissions := fixPermissions(zone.File); errPermissions != nil {
		Error("async buffer fix permissions error: %s: %v", zoneName, errPermissions)
	}

	PendingReload = true
	return nil
}

// rewriteWAL перезаписывает WAL так, чтобы его содержимое ровно совпадало
// с текущим содержимым b.pending. Выполняется ПОД ТЕМ ЖЕ мьютексом b.mu,
// что и Add, поэтому WAL и pending не могут разойтись.
//
// Файл открыт с O_APPEND: после Truncate(0) очередная запись уходит в начало.
func (b *AsyncRecordBuffer) rewriteWAL() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.walFile == nil {
		return
	}

	// Собираем актуальное содержимое из pending (единственный источник истины)
	var sb strings.Builder
	for zoneName, records := range b.pending {
		for _, record := range records {
			sb.WriteString(zoneName)
			sb.WriteString("|")
			sb.WriteString(record)
			sb.WriteString("\n")
		}
	}

	// Полностью обрезаем файл и записываем актуальное состояние
	if err := b.walFile.Truncate(0); err != nil {
		Error("Ошибка очистки WAL: %v", err)
		return
	}
	if _, err := b.walFile.WriteString(sb.String()); err != nil {
		Error("Ошибка перезаписи WAL: %v", err)
		return
	}
	if err := b.walFile.Sync(); err != nil {
		Error("Ошибка sync WAL: %v", err)
	}
}
