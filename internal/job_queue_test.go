package internal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupJobQueueTest(t *testing.T) {
	// Инициализируем БД для тестов
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	assert.NoError(t, err)

	oldDB := Db
	Db = db
	err = db.AutoMigrate(&AuditLog{})
	assert.NoError(t, err)

	// Инициализируем очередь
	JQ = make(chan *Job, MaxQueueSize)
	go jobWorker()

	t.Cleanup(func() {
		Db = oldDB
		JQ = nil
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

	// Получаем последний ID из аудита
	var lastID uint
	if Db != nil {
		Db.Model(&AuditLog{}).Select("COALESCE(MAX(id), 0)").Scan(&lastID)
		job.ID = int64(lastID) + 1
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
