package internal

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

func generateSecureKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// hashAPIKey создаёт bcrypt хеш из API-ключа
func hashAPIKey(key string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(key), 12)
	if err != nil {
		return "", fmt.Errorf("ошибка хеширования ключа: %v", err)
	}
	return string(hashed), nil
}

// verifyAPIKey проверяет соответствие ключа хешу
func verifyAPIKey(inputKey, storedHash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(inputKey))
	return err == nil
}

// generateKeyPrefix создаёт префикс для быстрого поиска ключа
// Префикс хранится в открытом виде и используется для поиска в БД
func generateKeyPrefix(key string) string {
	// Берём первые 12 символов ключа как префикс (например: "bindapi_abc123")
	if len(key) >= 12 {
		return key[:12]
	}
	return key
}

// ... existing code ...
