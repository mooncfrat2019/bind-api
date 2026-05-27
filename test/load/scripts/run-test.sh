#!/bin/bash
set -e

GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'
log_test() { echo -e "${BLUE}[TEST]${NC} $1"; }

API_KEY="test-api-key-12345"
API_URL="http://localhost:8080/api"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
RESULT_DIR="/app/results/${TIMESTAMP}"
mkdir -p "$RESULT_DIR"

run_test() {
    log_test "$1 (${2}s, ${3} RPS)"
    vegeta attack -duration="${2}s" -rate="$3" -targets="$4" -output="${RESULT_DIR}/${1}.bin"
    vegeta report -type=json "${RESULT_DIR}/${1}.bin" > "${RESULT_DIR}/${1}_report.json"
    vegeta report "${RESULT_DIR}/${1}.bin"
}

# Параметры
ZONE_NAME="loadtest-с.local"
RECORDS_COUNT=2000

log_test "=== Подготовка тестовых данных ==="
log_test "Зона: ${ZONE_NAME}"
log_test "Будет добавлено: ${RECORDS_COUNT} A-записей"

# Создание зоны (один запрос, не нагрузочный)
log_test "Создание зоны ${ZONE_NAME}..."
cat > /app/data/payloads/zone.json << EOF
{
  "name": "${ZONE_NAME}",
  "email": "admin@${ZONE_NAME}",
  "ns_ip": "192.168.1.1"
}
EOF

curl -s -X POST "${API_URL}/write/zone" \
    -H "Content-Type: application/json" \
    -H "X-API-Key: ${API_KEY}" \
    -d @/app/data/payloads/zone.json

echo ""
log_test "Зона создана"

# Подготовка JSON файлов для A-записей
log_test "Подготовка ${RECORDS_COUNT} JSON файлов для A-записей..."
for rec_num in $(seq 1 $RECORDS_COUNT); do
    ip="10.0.$((rec_num / 256)).$((rec_num % 256))"
    cat > "/app/data/payloads/record_${rec_num}.json" << EOF
{
  "name": "host-${rec_num}",
  "type": "A",
  "value": "${ip}",
  "ttl": 3600
}
EOF
done

# Подготовка targets файла для A-записей (с @file)
log_test "Подготовка targets файла..."
rm -f /tmp/targets_add.txt
for rec_num in $(seq 1 $RECORDS_COUNT); do
    cat >> /tmp/targets_add.txt << EOF
POST ${API_URL}/write/zone/${ZONE_NAME}/record
X-API-Key: ${API_KEY}
Content-Type: application/json
@/app/data/payloads/record_${rec_num}.json

EOF
done

# Запуск нагрузочного теста на добавление записей
log_test "Запуск нагрузочного теста: добавление ${RECORDS_COUNT} A-записей"
run_test "add_records" "120" "20" /tmp/targets_add.txt

echo ""
echo "=== Сводка результатов ==="
echo "Зона: ${ZONE_NAME}"
echo "A-записей добавлено: ${RECORDS_COUNT}"
echo ""

for f in ${RESULT_DIR}/*_report.json; do
    if [ -f "$f" ]; then
        name=$(basename "$f" _report.json)
        success=$(jq -r '.success' "$f" 2>/dev/null || echo "0")
        total=$(jq -r '.requests' "$f" 2>/dev/null || echo "0")
        avg=$(jq -r '.latencies.mean' "$f" 2>/dev/null || echo "0")
        p95=$(jq -r '.latencies["95th"]' "$f" 2>/dev/null || echo "0")
        echo "$name: успешно=$(echo "$success * 100" | bc)% ($total запросов), средняя=$avg мс, p95=$p95 мс"
    fi
done

echo ""
echo "=== Результаты сохранены в ${RESULT_DIR} ==="

# Очистка
rm -f /tmp/targets_*.txt
rm -f /app/data/payloads/zone.json
rm -f /app/data/payloads/record_*.json