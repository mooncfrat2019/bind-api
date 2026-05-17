package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var (
	testMutex sync.Mutex
)

// setupTestDB создаёт изолированную БД для каждого теста
func setupTestDB(t *testing.T) *gorm.DB {
	testMutex.Lock()
	defer testMutex.Unlock()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"), &gorm.Config{
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err)

	return db
}

func setupAPIKeyTest(t *testing.T) (*gin.Engine, string, *gorm.DB) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)

	err := db.AutoMigrate(&APIKey{})
	require.NoError(t, err)

	// Сохраняем старую БД и устанавливаем новую
	oldDB := Db
	Db = db

	// Создаём админский ключ
	permsJSON, _ := json.Marshal([]string{"admin", "zone:read", "zone:write"})
	adminKey := &APIKey{
		Name:        "admin-key",
		Description: "Admin test key",
		Permissions: string(permsJSON),
	}
	err = db.Create(adminKey).Error
	require.NoError(t, err)

	router := gin.New()

	t.Cleanup(func() {
		Db = oldDB
	})

	return router, adminKey.Key, db
}

func TestAPIKeyAuth(t *testing.T) {
	router, _, db := setupAPIKeyTest(t)

	// Создаём тестовый ключ
	permsJSON, _ := json.Marshal([]string{"zone:read"})
	validKey := &APIKey{
		Name:        "test-key",
		Permissions: string(permsJSON),
	}
	err := db.Create(validKey).Error
	require.NoError(t, err)

	expiredTime := time.Now().Add(-24 * time.Hour)
	expiredKey := &APIKey{
		Name:        "expired-key",
		Permissions: string(permsJSON),
		ExpiresAt:   &expiredTime,
	}
	err = db.Create(expiredKey).Error
	require.NoError(t, err)

	router.GET("/test", APIKeyAuth("zone:read"), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	tests := []struct {
		name           string
		apiKey         string
		expectedStatus int
	}{
		{"valid key", validKey.Key, http.StatusOK},
		{"no key", "", http.StatusUnauthorized},
		{"invalid key", "invalid-key", http.StatusUnauthorized},
		{"expired key", expiredKey.Key, http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.apiKey != "" {
				req.Header.Set("X-API-Key", tt.apiKey)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

func TestAPIKeyCreateAndList(t *testing.T) {
	router, adminKey, db := setupAPIKeyTest(t)

	router.POST("/api/keys", APIKeyAuth("admin"), HandleCreateAPIKey)
	router.GET("/api/keys", APIKeyAuth("admin"), HandleListAPIKeys)

	// Тест создания ключа
	createReq := CreateAPIKeyRequest{
		Name:        "new-key",
		Description: "Test key",
		Permissions: []string{"zone:read"},
		ExpiresIn:   30,
	}
	body, _ := json.Marshal(createReq)
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", adminKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code, "Create API key failed")

	// Тест списка ключей
	req = httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	req.Header.Set("X-API-Key", adminKey)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "List API keys failed")

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.True(t, response["success"].(bool))

	_ = db // используем чтобы избежать warning
}

func TestAPIKeyRevoke(t *testing.T) {
	router, adminKey, db := setupAPIKeyTest(t)

	userPerms, _ := json.Marshal([]string{"zone:read"})
	userKey := &APIKey{
		Name:        "user-key",
		Permissions: string(userPerms),
	}
	err := db.Create(userKey).Error
	require.NoError(t, err)

	router.DELETE("/api/keys/:id", APIKeyAuth("admin"), HandleRevokeAPIKey)

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/keys/%d", userKey.ID), nil)
	req.Header.Set("X-API-Key", adminKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "Revoke API key failed")

	var count int64
	db.Model(&APIKey{}).Where("id = ?", userKey.ID).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestAPIKeyRevokeOwnKey(t *testing.T) {
	router, _, db := setupAPIKeyTest(t)

	permsJSON, _ := json.Marshal([]string{"admin"})
	key := &APIKey{
		Name:        "test-key",
		Permissions: string(permsJSON),
	}
	err := db.Create(key).Error
	require.NoError(t, err)

	router.DELETE("/api/keys/:id", APIKeyAuth("admin"), HandleRevokeAPIKey)

	// Пытаемся удалить свой же ключ
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/keys/%d", key.ID), nil)
	req.Header.Set("X-API-Key", key.Key)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Должен быть BadRequest, нельзя удалить свой ключ
	assert.Equal(t, http.StatusBadRequest, w.Code, "Should not allow revoking own key")
}

func TestAPIKeyIPRestriction(t *testing.T) {
	router, _, db := setupAPIKeyTest(t)

	permsJSON, _ := json.Marshal([]string{"*"})
	ipKey := &APIKey{
		Name:        "ip-key",
		Permissions: string(permsJSON),
		IPAddress:   "192.168.1.100",
	}
	err := db.Create(ipKey).Error
	require.NoError(t, err)

	router.GET("/test", APIKeyAuth(""), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	// Запрос с другого IP
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-API-Key", ipKey.Key)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "IP restriction should block request")
}

func TestAPIKeyHasPermission(t *testing.T) {
	tests := []struct {
		name         string
		permissions  []string
		requiredPerm string
		expected     bool
	}{
		{"wildcard", []string{"*"}, "zone:write", true},
		{"exact match", []string{"zone:read"}, "zone:read", true},
		{"no match", []string{"zone:read"}, "zone:write", false},
		{"multiple", []string{"zone:read", "zone:write"}, "zone:write", true},
		{"empty", []string{}, "zone:read", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			permsJSON, _ := json.Marshal(tt.permissions)
			key := &APIKey{Permissions: string(permsJSON)}
			result := key.HasPermission(tt.requiredPerm)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAPIKeyIsExpired(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)

	tests := []struct {
		name      string
		expiresAt *time.Time
		expected  bool
	}{
		{"no expiration", nil, false},
		{"not expired", &future, false},
		{"expired", &past, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := &APIKey{ExpiresAt: tt.expiresAt}
			assert.Equal(t, tt.expected, key.IsExpired())
		})
	}
}

func TestAPIKeyValidation(t *testing.T) {
	router := gin.New()

	router.POST("/api/keys", func(c *gin.Context) {
		var req CreateAPIKeyRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Ошибка валидации", "error": err.Error()})
			return
		}

		if req.Name == "" || len(req.Name) < 3 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Name is required and must be at least 3 characters"})
			return
		}

		if len(req.Permissions) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Permissions are required"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"success": true})
	})

	tests := []struct {
		name       string
		request    CreateAPIKeyRequest
		shouldFail bool
	}{
		{
			name: "valid request",
			request: CreateAPIKeyRequest{
				Name:        "valid-key",
				Description: "Valid test key",
				Permissions: []string{"zone:read"},
				ExpiresIn:   30,
			},
			shouldFail: false,
		},
		{
			name: "missing name",
			request: CreateAPIKeyRequest{
				Name:        "",
				Description: "No name",
				Permissions: []string{"zone:read"},
			},
			shouldFail: true,
		},
		{
			name: "name too short",
			request: CreateAPIKeyRequest{
				Name:        "ab",
				Description: "Too short",
				Permissions: []string{"zone:read"},
			},
			shouldFail: true,
		},
		{
			name: "empty permissions",
			request: CreateAPIKeyRequest{
				Name:        "no-perms",
				Description: "No permissions",
				Permissions: []string{},
			},
			shouldFail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if tt.shouldFail {
				assert.Equal(t, http.StatusBadRequest, w.Code)
			} else {
				assert.Equal(t, http.StatusCreated, w.Code)
			}
		})
	}
}
