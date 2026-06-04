package internal

import (
	"log"
)

// MigrateExistingKeys мигрирует существующие ключи из plaintext в хеши
// Вызывать один раз при обновлении
func MigrateExistingKeys() error {
	if Db == nil {
		return nil
	}

	log.Println("Начало миграции API-ключей...")

	var keys []APIKey
	if err := Db.Find(&keys).Error; err != nil {
		return err
	}

	migrated := 0
	for _, key := range keys {
		// Если KeyHash пустой, значит ключ ещё в plaintext
		if key.KeyHash == "" && key.Key != "" {
			// Хешируем существующий ключ
			keyHash, err := hashAPIKey(key.Key)
			if err != nil {
				log.Printf("⚠️ Ошибка хеширования ключа ID=%d: %v", key.ID, err)
				continue
			}

			key.KeyHash = keyHash
			key.KeyPrefix = generateKeyPrefix(key.Key)

			if err := Db.Save(&key).Error; err != nil {
				log.Printf("⚠️ Ошибка сохранения ключа ID=%d: %v", key.ID, err)
				continue
			}

			migrated++
		}
	}

	if migrated > 0 {
		log.Printf("Миграция завершена: обновлено %d ключей", migrated)
	} else {
		log.Println("Миграция не требуется: все ключи уже хешированы")
	}

	return nil
}
