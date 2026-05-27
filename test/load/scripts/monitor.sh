#!/bin/bash
# scripts/monitor.sh

source .env.test 2>/dev/null || true

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log_monitor() { echo -e "${CYAN}[MONITOR]${NC} $(date +%H:%M:%S) | $1"; }

log_monitor "=== Мониторинг тестового окружения ==="

while true; do
    clear
    echo "=== BIND API Мониторинг ==="
    echo "Время: $(date)"
    echo ""

    # Статус MASTER
    echo "[MASTER]"
    if curl -s -f -H "X-API-Key: ${API_KEY:-test-api-key}" \
        "http://localhost:8080/api/status" 2>/dev/null | jq -r '.data | "  Статус BIND: " + .named_status + "\n  Очередь: " + (.queue_size|tostring) + "\n  Роль: " + .role' 2>/dev/null; then
        :
    else
        echo "  API недоступен"
    fi
    echo ""

    # Статус REPLICA
    echo "[REPLICA]"
    if curl -s -f "http://localhost:8081/api/sync/status" 2>/dev/null | jq -r '.data | "  Роль: " + .role + "\n  Синхронизация: " + (.sync_enabled|tostring) + "\n  Последняя синхронизация: " + .last_sync' 2>/dev/null; then
        :
    else
        echo "  API реплики недоступен"
    fi
    echo ""

    # Логи ошибок за последнюю минуту
    echo "[Ошибки в логах (последняя минута)]"
    docker logs test-master --since 1m 2>&1 | grep -E "ERROR|panic|FAILED" | tail -3 || echo "  Нет ошибок"
    echo ""

    # Использование ресурсов
    echo "[Ресурсы контейнеров]"
    docker stats --no-stream --format "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}" | grep -E "test-master|test-replica|test-postgres" || echo "  Не удалось получить статистику"
    echo ""

    sleep "${MONITOR_INTERVAL:-5}"
done