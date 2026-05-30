package internal

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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
			log.Printf("WARNING: Не удалось получить путь к исполняемому файлу: %v", err)
			execPath = "."
		}
		execDir := filepath.Dir(execPath)
		walPath = filepath.Join(execDir, "logs", "bind-api-wal.log")
	}

	// Создаем директорию для WAL если её нет
	walDir := filepath.Dir(walPath)
	if err := os.MkdirAll(walDir, 0755); err != nil {
		log.Printf("WARNING: Не удалось создать директорию для WAL %s: %v", walDir, err)
		// Fallback к временной директории
		walPath = filepath.Join(os.TempDir(), "bind-api-wal.log")
		os.MkdirAll(filepath.Dir(walPath), 0755)
	}

	walFile, err := os.OpenFile(walPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("WARNING: Не удалось создать WAL файл %s: %v", walPath, err)
		// Продолжаем без WAL (асинхронная запись без гарантий)
		walFile = nil
	} else {
		log.Printf("WAL файл: %s", walPath)
	}

	RecordBuffer = &AsyncRecordBuffer{
		pending:   make(map[string][]string),
		flushCh:   make(chan struct{}, 1),
		batchSize: BatchSize,
		interval:  BatchInterval,
		walFile:   walFile,
		walPath:   walPath,
	}

	// Восстанавливаем из WAL при старте
	if walFile != nil {
		RecordBuffer.recoverFromWAL()
	}

	go RecordBuffer.worker()
	log.Printf("Асинхронный буфер инициализирован: batchSize=%d, interval=%v, wal=%s",
		BatchSize, BatchInterval, walPath)
}

// recoverFromWAL восстанавливает данные из WAL после рестарта
func (b *AsyncRecordBuffer) recoverFromWAL() {
	if b.walFile == nil {
		return
	}

	// Синхронизируем и закрываем перед чтением
	b.walFile.Sync()
	b.walFile.Close()

	// Читаем WAL файл (используем os.ReadFile вместо ioutil.ReadFile)
	content, err := os.ReadFile(b.walPath)
	if err != nil {
		log.Printf("WARNING: Не удалось прочитать WAL файл %s: %v", b.walPath, err)
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

	// Очищаем WAL
	os.Truncate(b.walPath, 0)

	// Переоткрываем файл для записи
	walFile, err := os.OpenFile(b.walPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("WARNING: Не удалось переоткрыть WAL файл: %v", err)
		b.walFile = nil
	} else {
		b.walFile = walFile
	}

	if recoveredCount > 0 {
		log.Printf("🔄 Восстановлено %d записей из WAL", recoveredCount)
		go b.flush()
	}
}

// Add добавляет запись в буфер
func (b *AsyncRecordBuffer) Add(zoneName, recordLine string) {
	// Пишем в WAL для надежности
	if b.walFile != nil {
		walEntry := fmt.Sprintf("%s|%s\n", zoneName, recordLine)
		if _, err := b.walFile.WriteString(walEntry); err != nil {
			log.Printf("WARNING: Ошибка записи в WAL: %v", err)
		}
	}

	b.mu.Lock()
	b.pending[zoneName] = append(b.pending[zoneName], recordLine)
	batchSize := len(b.pending[zoneName])
	b.mu.Unlock()

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

func (b *AsyncRecordBuffer) flush() {
	b.mu.Lock()
	toFlush := b.pending
	b.pending = make(map[string][]string)
	b.mu.Unlock()

	for zoneName, records := range toFlush {
		go b.writeToZone(zoneName, records) // Параллельная запись
	}
}

func (b *AsyncRecordBuffer) writeToZone(zoneName string, records []string) {
	zone, exists := getZoneFromConfig(zoneName)
	if !exists {
		return
	}

	err := withFileLock(zone.File, func() error {
		for _, record := range records {
			errAppend := appendRecordToFile(zone.File, record)
			if errAppend != nil {
				log.Printf("async buffer append record error: %s, %v, %v", zoneName, records, errAppend)
			}
		}
		errSerial := incrementSerial(zone.File)
		if errSerial != nil {
			log.Printf("async buffer increment serial error: %s, %v, %v", zoneName, records, errSerial)
		}
		return nil
	})
	if err != nil {
		log.Printf("async buffer record error: %s, %v, %v", zoneName, records, err)
	}

	errPermissions := fixPermissions(zone.File)
	log.Printf("async buffer error permissions error: %s, %v, %v", zoneName, records, errPermissions)
	PendingReload = true
}
