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

# Создание API-ключа
log_info "Создание API-ключа в БД..."
PGPASSWORD=test_password psql -h postgres -U test_user -d dns_test << EOF
INSERT INTO api_keys (key, name, description, permissions, created_at, updated_at)
VALUES ('test-api-key-12345', 'test-key', 'Test API key', '["*"]', NOW(), NOW())
ON CONFLICT (key) DO UPDATE SET permissions = '["*"]', updated_at = NOW();
EOF

log_info "Настройка завершена"