package internal

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsCollector - централизованный сборщик метрик
type MetricsCollector struct {
	// Бизнес-метрики
	ZonesTotal        *prometheus.GaugeVec
	RecordsTotal      *prometheus.GaugeVec
	OperationsTotal   *prometheus.CounterVec
	OperationDuration *prometheus.HistogramVec

	// Технические метрики
	QueueSize    prometheus.Gauge
	QueueMode    prometheus.Gauge
	BindStatus   prometheus.Gauge
	WorkerActive prometheus.Gauge

	// Метрики синхронизации (Master)
	SyncFilesTotal    prometheus.Gauge
	SyncVersionsTotal prometheus.Gauge

	// Метрики репликации (Replica)
	ReplicaLag          prometheus.Gauge
	ReplicaRetransfers  *prometheus.CounterVec
	ReplicaARecordCheck *prometheus.CounterVec

	// HTTP метрики
	HTTPRequests *prometheus.CounterVec
	HTTPDuration *prometheus.HistogramVec

	// Системные метрики
	APIDBKeysTotal prometheus.Gauge
	AuditLogsTotal prometheus.Gauge
	DBConnections  prometheus.Gauge

	// Внутренние счётчики
	mu           sync.RWMutex
	startTime    time.Time
	lastSyncTime map[string]time.Time
}

var Metrics *MetricsCollector

// InitMetrics инициализирует систему метрик
func InitMetrics() {
	Metrics = &MetricsCollector{
		startTime:    time.Now(),
		lastSyncTime: make(map[string]time.Time),
	}

	// Регистрируем метрики

	// Бизнес-метрики
	Metrics.ZonesTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bind_zones_total",
			Help: "Total number of DNS zones",
		},
		[]string{"type", "role"},
	)

	Metrics.RecordsTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bind_records_total",
			Help: "Total number of DNS records by type",
		},
		[]string{"record_type", "zone"},
	)

	Metrics.OperationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bind_operations_total",
			Help: "Total number of API operations",
		},
		[]string{"operation", "status"},
	)

	Metrics.OperationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "bind_operation_duration_seconds",
			Help:    "Duration of operations",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"operation"},
	)

	// Технические метрики
	Metrics.QueueSize = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "bind_queue_size",
			Help: "Current size of job queue",
		},
	)

	Metrics.QueueMode = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "bind_queue_mode",
			Help: "Queue mode: 0=normal, 1=batch",
		},
	)

	Metrics.BindStatus = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "bind_named_status",
			Help: "BIND named status: 1=active, 0=inactive",
		},
	)

	Metrics.WorkerActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "bind_workers_active",
			Help: "Number of active workers",
		},
	)

	// Метрики синхронизации
	Metrics.SyncFilesTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "bind_sync_files_total",
			Help: "Total number of files tracked in sync state",
		},
	)

	Metrics.SyncVersionsTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "bind_sync_versions_total",
			Help: "Total number of versions stored",
		},
	)

	// Метрики репликации
	Metrics.ReplicaLag = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "bind_replica_lag_seconds",
			Help: "Replication lag in seconds",
		},
	)

	Metrics.ReplicaRetransfers = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bind_replica_retransfers_total",
			Help: "Number of zone retransfers performed",
		},
		[]string{"zone", "status"},
	)

	Metrics.ReplicaARecordCheck = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bind_replica_arecord_check_total",
			Help: "Results of A record resolution checks",
		},
		[]string{"zone", "result"},
	)

	// HTTP метрики
	Metrics.HTTPRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bind_http_requests_total",
			Help: "Total HTTP requests",
		},
		[]string{"method", "endpoint", "status"},
	)

	Metrics.HTTPDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "bind_http_duration_seconds",
			Help:    "HTTP request duration",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)

	// Системные метрики
	Metrics.APIDBKeysTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "bind_apikeys_total",
			Help: "Total number of API keys",
		},
	)

	Metrics.AuditLogsTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "bind_audit_logs_total",
			Help: "Total number of audit log entries",
		},
	)

	Metrics.DBConnections = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "bind_db_connections",
			Help: "Number of active database connections",
		},
	)

	Info("Metrics system initialized")
}

// MetricsMiddleware - middleware для сбора HTTP метрик
func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// Обрабатываем запрос
		c.Next()

		// Собираем метрики
		duration := time.Since(start).Seconds()
		status := fmt.Sprintf("%d", c.Writer.Status())
		endpoint := c.FullPath()
		if endpoint == "" {
			endpoint = c.Request.URL.Path
		}

		Metrics.HTTPRequests.WithLabelValues(
			c.Request.Method,
			endpoint,
			status,
		).Inc()

		Metrics.HTTPDuration.WithLabelValues(
			c.Request.Method,
			endpoint,
		).Observe(duration)
	}
}

// UpdateQueueMetrics обновляет метрики очереди
func (m *MetricsCollector) UpdateQueueMetrics() {
	if AppRole == "master" {
		m.QueueSize.Set(float64(len(JQ)))

		ModeMutex.RLock()
		mode := CurrentMode
		ModeMutex.RUnlock()

		if mode == "batch" {
			m.QueueMode.Set(1)
		} else {
			m.QueueMode.Set(0)
		}
	}
}

// UpdateBusinessMetrics обновляет бизнес-метрики
func (m *MetricsCollector) UpdateBusinessMetrics() {
	if AppRole != "master" {
		return
	}

	zones, err := parseZoneConfig()
	if err != nil {
		Error("Failed to update zone metrics: %v", err)
		return
	}

	// Подсчёт зон по типам
	var forwardZones, reverseZones int
	for _, zone := range zones {
		if zone.Type == "forward" {
			forwardZones++
		} else {
			reverseZones++
		}

		// Подсчёт записей в зоне
		records, err := readZoneFileSimple(zone.File)
		if err == nil {
			recordCount := make(map[string]int)
			for _, rec := range records {
				recordCount[rec.Type]++
			}

			// Обновляем метрики для каждой зоны
			for recType, count := range recordCount {
				m.RecordsTotal.WithLabelValues(recType, zone.Name).Set(float64(count))
			}
		}
	}

	m.ZonesTotal.WithLabelValues("forward", AppRole).Set(float64(forwardZones))
	m.ZonesTotal.WithLabelValues("reverse", AppRole).Set(float64(reverseZones))
}

// UpdateSyncMetrics обновляет метрики синхронизации
func (m *MetricsCollector) UpdateSyncMetrics() {
	if AppRole != "master" || Db == nil {
		return
	}

	var fileCount int64
	var versionCount int64
	var apiKeyCount int64
	var auditCount int64

	Db.Model(&SyncState{}).Distinct("file_name").Count(&fileCount)
	Db.Model(&SyncState{}).Count(&versionCount)
	Db.Model(&APIKey{}).Count(&apiKeyCount)
	Db.Model(&AuditLog{}).Count(&auditCount)

	m.SyncFilesTotal.Set(float64(fileCount))
	m.SyncVersionsTotal.Set(float64(versionCount))
	m.APIDBKeysTotal.Set(float64(apiKeyCount))
	m.AuditLogsTotal.Set(float64(auditCount))
}

// UpdateReplicaMetrics обновляет метрики реплики
func (m *MetricsCollector) UpdateReplicaMetrics() {
	if AppRole != "replica" || RS == nil {
		return
	}

	lastSync := RS.GetLastSyncTime()
	if !lastSync.IsZero() {
		lag := time.Since(lastSync).Seconds()
		m.ReplicaLag.Set(lag)
	}
}

// RecordOperation записывает метрику операции
func (m *MetricsCollector) RecordOperation(opType string, duration time.Duration, err error) {
	m.OperationsTotal.WithLabelValues(opType, "total").Inc()
	m.OperationDuration.WithLabelValues(opType).Observe(duration.Seconds())

	if err == nil {
		m.OperationsTotal.WithLabelValues(opType, "success").Inc()
	} else {
		m.OperationsTotal.WithLabelValues(opType, "failed").Inc()
	}
}

// RecordReplicaCheck записывает результат проверки A записи
func (m *MetricsCollector) RecordReplicaCheck(zoneName string, success bool) {
	result := "success"
	if !success {
		result = "failed"
	}
	m.ReplicaARecordCheck.WithLabelValues(zoneName, result).Inc()
}

// RecordReplicaRetransfer записывает операцию retransfer
func (m *MetricsCollector) RecordReplicaRetransfer(zoneName string, err error) {
	status := "success"
	if err != nil {
		status = "failed"
	}
	m.ReplicaRetransfers.WithLabelValues(zoneName, status).Inc()
}

// StartMetricsUpdater запускает периодическое обновление метрик
func StartMetricsUpdater() {
	if Metrics == nil {
		return
	}

	// Обновляем метрики каждые 30 секунд
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		for range ticker.C {
			Metrics.UpdateQueueMetrics()
			Metrics.UpdateBusinessMetrics()
			Metrics.UpdateSyncMetrics()
			Metrics.UpdateReplicaMetrics()

			// Обновляем статус BIND
			cmd := exec.Command("systemctl", "is-active", "named")
			if err := cmd.Run(); err == nil {
				Metrics.BindStatus.Set(1)
			} else {
				Metrics.BindStatus.Set(0)
			}

			// Обновляем соединения с БД
			if Db != nil {
				sqlDB, err := Db.DB()
				if err == nil {
					stats := sqlDB.Stats()
					Metrics.DBConnections.Set(float64(stats.OpenConnections))
				}
			}
		}
	}()

	Info("Metrics updater started (interval: 30s)")
}

// MetricsHandler возвращает HTTP хендлер для Prometheus
func MetricsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := promhttp.Handler()
		h.ServeHTTP(c.Writer, c.Request)
	}
}

// HealthHandler возвращает расширенный health check с метриками
func HealthHandler(c *gin.Context) {
	health := gin.H{
		"status":    "healthy",
		"role":      AppRole,
		"uptime":    time.Since(Metrics.startTime).String(),
		"timestamp": time.Now(),
	}

	if AppRole == "master" {
		health["queue"] = gin.H{
			"size":     len(JQ),
			"max_size": MaxQueueSize,
			"mode":     CurrentMode,
		}

		if Db != nil {
			sqlDB, _ := Db.DB()
			stats := sqlDB.Stats()
			health["database"] = gin.H{
				"open_connections": stats.OpenConnections,
				"in_use":           stats.InUse,
				"idle":             stats.Idle,
			}
		}

		var zoneCount int64
		var recordCount int64
		if zones, err := parseZoneConfig(); err == nil {
			zoneCount = int64(len(zones))
			for _, zone := range zones {
				if records, err := readZoneFileSimple(zone.File); err == nil {
					recordCount += int64(len(records))
				}
			}
		}

		health["business"] = gin.H{
			"zones":   zoneCount,
			"records": recordCount,
		}
	} else if AppRole == "replica" && RS != nil {
		health["replication"] = gin.H{
			"last_sync":     RS.GetLastSyncTime(),
			"files_updated": RS.GetFilesUpdatedCount(),
			"master_url":    os.Getenv("MASTER_URL"),
			"sync_interval": os.Getenv("SYNC_INTERVAL"),
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    health,
	})
}

// RecordHTTPRequest - вспомогательная функция для записи HTTP метрик
func RecordHTTPRequest(method, endpoint, status string, duration float64) {
	if Metrics != nil {
		Metrics.HTTPRequests.WithLabelValues(method, endpoint, status).Inc()
		Metrics.HTTPDuration.WithLabelValues(method, endpoint).Observe(duration)
	}
}
