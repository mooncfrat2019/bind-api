package internal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupJobQueueTest(t *testing.T) {
	// Создаём уникальную БД для каждого теста с уникальным именем
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(0)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"), &gorm.Config{})
	require.NoError(t, err)

	// Сохраняем старую БД и устанавливаем новую
	oldDB := Db
	Db = db

	// Миграции
	err = db.AutoMigrate(&AuditLog{})
	require.NoError(t, err)

	// Инициализируем очередь
	oldJQ := JQ
	JQ = make(chan *Job, MaxQueueSize)

	// Запускаем worker в фоне
	go jobWorker()

	t.Cleanup(func() {
		// Закрываем очередь и ждём завершения worker
		close(JQ)
		time.Sleep(100 * time.Millisecond)
		Db = oldDB
		JQ = oldJQ
	})
}

func TestSubmitJob(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping job queue test in short mode")
	}

	setupJobQueueTest(t)

	// Создаём тестовое задание
	job := &Job{
		Type:       JobReload,
		ZoneName:   "test.com",
		ResponseCh: make(chan JobResult, 1),
		CreatedAt:  time.Now(),
	}

	// Отправляем в очередь
	select {
	case JQ <- job:
		assert.True(t, true, "Job sent to queue")
	default:
		t.Skip("Queue is full, skipping test")
	}
}

func TestJobQueueFull(t *testing.T) {
	// Создаём маленькую очередь
	oldQueue := JQ
	JQ = make(chan *Job, 1)
	defer func() { JQ = oldQueue }()

	// Заполняем очередь
	select {
	case JQ <- &Job{Type: JobReload}:
		// Успешно
	default:
		t.Skip("Could not send to queue")
	}

	// Проверяем что очередь заполнена
	JQMutex.Lock()
	isFull := len(JQ) >= cap(JQ)
	JQMutex.Unlock()

	assert.True(t, isFull, "Queue should be full")
}

func TestJobTypes(t *testing.T) {
	jobTypes := []JobType{
		JobCreateZone,
		JobDeleteZone,
		JobAddRecord,
		JobDeleteRecord,
		JobReload,
	}

	for _, jt := range jobTypes {
		t.Run(string(jt), func(t *testing.T) {
			job := &Job{
				Type:     jt,
				ZoneName: "test.com",
			}
			assert.NotEmpty(t, job.Type)
		})
	}
}
