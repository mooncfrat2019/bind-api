#!/bin/bash
set -e

GREEN='\033[0;32m'
NC='\033[0m'
log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }

log_info "=== Настройка тестового окружения ==="

# Создание тестовых данных
mkdir -p /app/data/payloads
cat > /app/data/payloads/create_zone.json << 'EOF'
{
  "name": "test-zone.local",
  "email": "admin@test-zone.local",
  "ns_ip": "192.168.1.1"
}
EOF

# Создание API-ключа с хешированием
log_info "Создание API-ключа в БД..."

#  ПЛАИНТЕКС КЛЮЧ (используется в тестах)
TEST_API_KEY="test-api-key-12345"

# ПРЕДВАРИТЕЛЬНО ВЫЧИСЛЕННЫЙ BCRYPT ХЕШ
# Хеш детерминированный для тестов (cost=10)
TEST_KEY_HASH='$2a$12$2R/TUkPxyCtpcsygQvBNyOH1G9LGjAEuzKOFJyTrQRc/Ww9/ugs36'

#  ПРЕФИКС ДЛЯ ПОИСКА (первые 12 символов ключа)
TEST_KEY_PREFIX="${TEST_API_KEY:0:12}"

PGPASSWORD=test_password psql -h postgres -U test_user -d dns_test << EOF
INSERT INTO api_keys (key, key_hash, key_prefix, name, description, permissions, created_at, updated_at)
VALUES (
    '-',
    '${TEST_KEY_HASH}',
    '${TEST_KEY_PREFIX}',
    'test-key',
    'Test API key for load testing',
    '["*"]',
    NOW(),
    NOW()
)
ON CONFLICT (key_hash) DO UPDATE
SET permissions = '["*"]', updated_at = NOW();
EOF

log_info "API-ключ создан:"
log_info "  Plaintext (для тестов): ${TEST_API_KEY}"
log_info "  Prefix: ${TEST_KEY_PREFIX}"
log_info "  Hash: ${TEST_KEY_HASH:0:20}..."

log_info "Настройка завершена"