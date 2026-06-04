package internal

import (
	"log"
)

// MigrateExistingKeys мигрирует существующие ключи из plaintext в хеши
// Вызывается один раз при обновлении системы
func MigrateExistingKeys() error {
	if Db == nil {
		return nil
	}

	log.Println("🔄 Проверка миграции API-ключей...")

	var keys []APIKey
	if err := Db.Find(&keys).Error; err != nil {
		return err
	}

	migrated := 0
	skipped := 0

	for _, key := range keys {
		// Если KeyHash пустой, значит ключ ещё в plaintext (старый формат)
		if key.KeyHash == "" {
			// Проверяем есть ли старый ключ в поле Key
			if key.Key != "" {
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
				log.Printf("✓ Мигрирован ключ ID=%d, Name=%s", key.ID, key.Name)
			} else {
				// Ключа нет вообще - пропускаем
				skipped++
			}
		} else {
			// Ключ уже хеширован
			skipped++
		}
	}

	if migrated > 0 {
		log.Printf("Миграция завершена: обновлено %d ключей, пропущено %d", migrated, skipped)
	} else {
		log.Printf("Миграция не требуется: все %d ключей уже хешированы", len(keys))
	}

	return nil
}
