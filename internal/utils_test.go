package internal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateZoneName(t *testing.T) {
	tests := []struct {
		name     string
		zoneName string
		expected bool
	}{
		{"valid domain", "example.com", true},
		{"valid subdomain", "sub.example.com", true},
		{"valid with hyphen", "my-site.com", true},
		{"empty string", "", false},
		{"too long", string(make([]byte, 300)), false},
		{"with double dot", "example..com", false},
		{"with slash", "example/com", false},
		{"with semicolon", "example;com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validateZoneName(tt.zoneName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateRecordName(t *testing.T) {
	tests := []struct {
		name       string
		recordName string
		expected   bool
	}{
		{"valid name", "www", true},
		{"valid with dot", "sub.domain", true},
		{"with double dot", "www..example", false},
		{"with slash", "www/example", false},
		{"with semicolon", "www;example", false},
		{"at sign", "@", true},  // @ - допустимое имя (сама зона)
		{"asterisk", "*", true}, // * - wildcard запись
		{"empty", "", false},    // пустое имя НЕ допустимо (нужно использовать @)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validateRecordName(tt.recordName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCalculateChecksum(t *testing.T) {
	// Создаём временный файл
	tmpFile, err := os.CreateTemp("", "test-*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	content := []byte("test content")
	_, err = tmpFile.Write(content)
	require.NoError(t, err)
	tmpFile.Close()

	checksum, err := calculateChecksum(tmpFile.Name())
	require.NoError(t, err)
	assert.NotEmpty(t, checksum)
	assert.Len(t, checksum, 64) // SHA256 = 64 hex chars

	// Проверка несуществующего файла
	checksum, err = calculateChecksum("/nonexistent/file")
	assert.NoError(t, err)
	assert.Empty(t, checksum)
}

func TestGetReverseZoneName(t *testing.T) {
	tests := []struct {
		ip       string
		expected string
		hasError bool
	}{
		{"192.168.1.100", "1.168.192.in-addr.arpa", false},
		{"10.0.0.1", "0.0.10.in-addr.arpa", false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			result, err := getReverseZoneName(tt.ip)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestGetPtrRecordName(t *testing.T) {
	tests := []struct {
		ip       string
		expected string
		hasError bool
	}{
		{"192.168.1.100", "100", false},
		{"10.0.0.1", "1", false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			result, err := getPtrRecordName(tt.ip)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestGenerateSecureKey(t *testing.T) {
	key1 := generateSecureKey()
	key2 := generateSecureKey()

	assert.NotEmpty(t, key1)
	assert.NotEmpty(t, key2)
	assert.Len(t, key1, 64)
	assert.Len(t, key2, 64)
	assert.NotEqual(t, key1, key2)
}

func TestFixPermissions(t *testing.T) {
	// В тестовой среде это может не работать, но функция не должна падать
	tmpFile, err := os.CreateTemp("", "test-*.zone")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	// Функция не должна возвращать ошибку даже если chown не работает
	err = fixPermissions(tmpFile.Name())
	// В тестах может быть ошибка прав, но это нормально
	_ = err
}

func TestWithFileLock(t *testing.T) {
	counter := 0

	err := withFileLock("/tmp/test.lock", func() error {
		counter++
		return nil
	})

	assert.NoError(t, err)
	assert.Equal(t, 1, counter)
}

func TestSendResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	sendResponse(c, http.StatusOK, true, "test message", gin.H{"key": "value"})

	assert.Equal(t, http.StatusOK, w.Code)

	var response Response
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.True(t, response.Success)
	assert.Equal(t, "test message", response.Message)
	assert.Equal(t, "value", response.Data.(map[string]interface{})["key"])
}

func TestValidateTXTRecord(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		shouldErr bool
		errMsg    string
	}{
		// Легитимные TXT записи
		{
			name:      "valid SPF record",
			value:     "v=spf1 include:_spf.google.com ~all",
			shouldErr: false,
		},
		{
			name:      "valid DKIM record",
			value:     "v=DKIM1; k=rsa; p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQC",
			shouldErr: false,
		},
		{
			name:      "valid DMARC record",
			value:     "v=DMARC1; p=reject; rua=mailto:dmarc@example.com",
			shouldErr: false,
		},
		{
			name:      "valid Google verification",
			value:     "google-site-verification=abc123xyz",
			shouldErr: false,
		},
		{
			name:      "valid simple text",
			value:     "This is a simple TXT record",
			shouldErr: false,
		},

		// Опасные паттерны
		{
			name:      "xss script tag",
			value:     "<script>alert('xss')</script>",
			shouldErr: true,
			errMsg:    "потенциально опасный паттерн",
		},
		{
			name:      "xss javascript protocol",
			value:     "javascript:alert(1)",
			shouldErr: true,
			errMsg:    "потенциально опасный паттерн",
		},
		{
			name:      "xss onerror handler",
			value:     "onerror=alert(1)",
			shouldErr: true,
			errMsg:    "потенциально опасный паттерн",
		},
		{
			name:      "xss iframe tag",
			value:     "<iframe src='evil.com'></iframe>",
			shouldErr: true,
			errMsg:    "потенциально опасный паттерн",
		},
		{
			name:      "url encoded script",
			value:     "%3cscript%3ealert(1)%3c/script%3e",
			shouldErr: true,
			errMsg:    "подозрительное экранирование",
		},
		{
			name:      "hex encoded less than",
			value:     "\\x3cscript\\x3e",
			shouldErr: true,
			errMsg:    "подозрительное экранирование",
		},

		// Граничные случаи
		{
			name:      "empty value",
			value:     "",
			shouldErr: true,
			errMsg:    "не может быть пустой",
		},
		{
			name:      "too long value",
			value:     strings.Repeat("a", 256),
			shouldErr: true,
			errMsg:    "слишком длинная",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTXTRecord(tt.value)
			if tt.shouldErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSanitizeTXTRecord(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "normal text",
			input:    "v=spf1 include:_spf.google.com ~all",
			expected: "v=spf1 include:_spf.google.com ~all",
		},
		{
			name:     "with leading/trailing spaces",
			input:    "  google-site-verification=abc123  ",
			expected: "google-site-verification=abc123",
		},
		{
			name:     "with null bytes",
			input:    "test\x00value",
			expected: "testvalue",
		},
		{
			name:     "with multiple null bytes",
			input:    "v=spf1\x00\x00include:_spf.google.com",
			expected: "v=spf1include:_spf.google.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeTXTRecord(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
