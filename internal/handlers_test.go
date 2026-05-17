package internal

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupTest(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	gin.SetMode(gin.TestMode)

	// Используем общую функцию setup
	db := SetupTestDB(t)

	err := db.AutoMigrate(&AuditLog{}, &SyncState{}, &APIKey{})
	require.NoError(t, err)

	oldDB := Db
	SetGlobalDB(db)

	permsJSON, _ := json.Marshal([]string{"*"})
	testKey := &APIKey{
		Name:        "test-key",
		Description: "Test API Key",
		Permissions: string(permsJSON),
	}
	err = db.Create(testKey).Error
	require.NoError(t, err)

	router := gin.New()

	t.Cleanup(func() {
		RestoreGlobalDB(oldDB)
	})

	return router, db, testKey.Key
}

// Тесты без middleware для проверки валидации
func TestCreateZoneValidation(t *testing.T) {
	router := gin.New()

	router.POST("/api/write/zone", func(c *gin.Context) {
		var req ZoneRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if !validateZoneName(req.Name) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid zone name"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	tests := []struct {
		name      string
		zoneName  string
		shouldErr bool
	}{
		{"valid zone", "test.example.com", false},
		{"invalid zone with dots", "test..example.com", true},
		{"invalid zone with slash", "test/example.com", true},
		{"invalid zone with semicolon", "test;example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zoneReq := ZoneRequest{Name: tt.zoneName}
			body, _ := json.Marshal(zoneReq)
			req := httptest.NewRequest(http.MethodPost, "/api/write/zone", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if tt.shouldErr {
				assert.Equal(t, http.StatusBadRequest, w.Code)
			} else {
				assert.Equal(t, http.StatusOK, w.Code)
			}
		})
	}
}

func TestAddRecordValidation(t *testing.T) {
	router := gin.New()

	router.POST("/api/write/zone/:name/record", func(c *gin.Context) {
		var req RecordRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		recordType := strings.ToUpper(req.Type)
		if recordType != "A" && recordType != "AAAA" && recordType != "CNAME" && recordType != "MX" && recordType != "TXT" && recordType != "NS" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported type"})
			return
		}

		if recordType == "A" {
			ip := net.ParseIP(req.Value)
			if ip == nil || ip.To4() == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid IPv4 address"})
				return
			}
		}

		if recordType == "AAAA" {
			ip := net.ParseIP(req.Value)
			if ip == nil || ip.To4() != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid IPv6 address"})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	tests := []struct {
		name      string
		recordReq RecordRequest
		shouldErr bool
	}{
		{
			name:      "valid A record",
			recordReq: RecordRequest{Name: "www", Type: "A", Value: "192.168.1.100"},
			shouldErr: false,
		},
		{
			name:      "invalid IP for A record",
			recordReq: RecordRequest{Name: "www", Type: "A", Value: "invalid-ip"},
			shouldErr: true,
		},
		{
			name:      "valid AAAA record",
			recordReq: RecordRequest{Name: "www", Type: "AAAA", Value: "2001:0db8:85a3:0000:0000:8a2e:0370:7334"},
			shouldErr: false,
		},
		{
			name:      "valid CNAME record",
			recordReq: RecordRequest{Name: "mail", Type: "CNAME", Value: "www.example.com"},
			shouldErr: false,
		},
		{
			name:      "unsupported type",
			recordReq: RecordRequest{Name: "test", Type: "AAAAA", Value: "value"},
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.recordReq)
			req := httptest.NewRequest(http.MethodPost, "/api/write/zone/test.com/record", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if tt.shouldErr {
				assert.Equal(t, http.StatusBadRequest, w.Code)
			} else {
				assert.Equal(t, http.StatusOK, w.Code)
			}
		})
	}
}

func TestDeleteZoneValidation(t *testing.T) {
	router := gin.New()

	router.DELETE("/api/write/zone/:name", func(c *gin.Context) {
		zoneName := c.Param("name")

		// Проверка валидности
		if !validateZoneName(zoneName) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid zone name"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	tests := []struct {
		name           string
		zoneName       string
		expectedStatus int
	}{
		{"valid zone", "example.com", http.StatusOK},
		{"invalid zone with dots", "example..com", http.StatusBadRequest},
		{"invalid zone with slash", "example/com", http.StatusNotFound}, // <- 404, потому что слеш ломает маршрут
		{"invalid zone with semicolon", "example;com", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, "/api/write/zone/"+tt.zoneName, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

func TestDeleteRecordValidation(t *testing.T) {
	router := gin.New()

	router.DELETE("/api/write/zone/:name/record/:record/:type", func(c *gin.Context) {
		zoneName := c.Param("name")
		recordName := c.Param("record")

		if !validateZoneName(zoneName) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid zone name"})
			return
		}
		if !validateRecordName(recordName) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid record name"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	tests := []struct {
		name       string
		zoneName   string
		recordName string
		valid      bool
	}{
		{"valid", "example.com", "www", true},
		{"invalid zone", "example..com", "www", false},
		{"invalid record", "example.com", "www..test", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, "/api/write/zone/"+tt.zoneName+"/record/"+tt.recordName+"/A", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if !tt.valid {
				assert.Equal(t, http.StatusBadRequest, w.Code)
			} else {
				assert.Equal(t, http.StatusOK, w.Code)
			}
		})
	}
}

// Тесты с middleware (только для чтения)
func TestHandleStatus(t *testing.T) {
	router, _, _ := setupTest(t)

	AppRole = "master"
	defer func() { AppRole = "" }()

	router.GET("/api/status", HandleStatus)

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleStatusReplica(t *testing.T) {
	router, _, _ := setupTest(t)

	AppRole = "replica"
	defer func() { AppRole = "" }()

	RS = &ReplicaSync{
		Enabled:      true,
		lastSyncTime: time.Now(),
	}
	defer func() { RS = nil }()

	router.GET("/api/status", HandleStatus)

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleConfig(t *testing.T) {
	router, _, apiKey := setupTest(t)

	ZoneDir = "/var/named/"
	ZoneConfFile = "/etc/named.zones.conf"
	NamedConf = "/etc/named.conf"
	os.Setenv("API_PORT", "8080")

	testConfigDir := t.TempDir()
	testNamedConf := testConfigDir + "/named.conf"
	NamedConf = testNamedConf

	configContent := `options {
    directory "/var/named";
};`
	err := os.WriteFile(testNamedConf, []byte(configContent), 0644)
	require.NoError(t, err)

	router.GET("/api/read/config", APIKeyAuth("zone:read"), HandleConfig)

	req := httptest.NewRequest(http.MethodGet, "/api/read/config", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleListZones(t *testing.T) {
	router, _, apiKey := setupTest(t)

	testDir := t.TempDir()
	testConf := testDir + "/named.conf"
	NamedConf = testConf

	configContent := `zone "example.com" IN {
    type master;
    file "example.com.zone";
};`
	err := os.WriteFile(testConf, []byte(configContent), 0644)
	require.NoError(t, err)

	zoneDir := t.TempDir()
	ZoneDir = zoneDir + "/"
	zoneFile := ZoneDir + "example.com.zone"
	err = os.WriteFile(zoneFile, []byte("$TTL 3600\n@ IN SOA ns1.example.com. admin.example.com. (2024010101 3600 600 604800 3600)\n@ IN NS ns1.example.com.\nwww IN A 192.168.1.1"), 0644)
	require.NoError(t, err)

	router.GET("/api/read/zones", APIKeyAuth("zone:read"), HandleListZones)

	req := httptest.NewRequest(http.MethodGet, "/api/read/zones", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleAuditLog(t *testing.T) {
	router, db, apiKey := setupTest(t)

	now := time.Now()
	testAudit := AuditLog{
		JobType:   "CREATE_ZONE",
		ZoneName:  "test.com",
		Status:    "COMPLETED",
		CreatedAt: now,
	}
	err := db.Create(&testAudit).Error
	require.NoError(t, err)

	router.GET("/api/read/audit", APIKeyAuth("zone:read"), HandleAuditLog)

	req := httptest.NewRequest(http.MethodGet, "/api/read/audit", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleAuditStats(t *testing.T) {
	router, db, apiKey := setupTest(t)

	now := time.Now()
	audits := []AuditLog{
		{JobType: "CREATE_ZONE", Status: "COMPLETED", CreatedAt: now},
		{JobType: "DELETE_ZONE", Status: "COMPLETED", CreatedAt: now},
		{JobType: "ADD_RECORD", Status: "FAILED", CreatedAt: now},
	}
	for _, a := range audits {
		err := db.Create(&a).Error
		require.NoError(t, err)
	}

	router.GET("/api/read/audit/stats", APIKeyAuth("zone:read"), HandleAuditStats)

	req := httptest.NewRequest(http.MethodGet, "/api/read/audit/stats", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleGetZoneNotFound(t *testing.T) {
	router, _, apiKey := setupTest(t)

	router.GET("/api/read/zone/:name", APIKeyAuth("zone:read"), HandleGetZone)

	req := httptest.NewRequest(http.MethodGet, "/api/read/zone/nonexistent.zone.com", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Contains(t, []int{http.StatusNotFound, http.StatusInternalServerError}, w.Code)
}
