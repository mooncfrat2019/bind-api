#!/bin/bash
set -e

GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_test() { echo -e "${BLUE}[TEST]${NC} $1"; }

API_KEY="${API_KEY:-test-api-key-12345}"
API_URL="${API_URL:-http://localhost:8080/api}"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
RESULT_DIR="/app/results/${TIMESTAMP}"
mkdir -p "$RESULT_DIR"

# Параметры тестирования
ZONE_NAME="${ZONE_NAME:-loadtest-$(date +%s).local}"
DURATION="${DURATION:-30}"
RPS="${RPS:-10}"
BATCH_SIZE="${BATCH_SIZE:-50}"
TEST_TYPE="${TEST_TYPE:-add_records}"
CLEANUP="${CLEANUP:-true}"

if [ "$TEST_TYPE" = "batch" ]; then
    RECORDS_COUNT=$((DURATION * RPS))
else
    RECORDS_COUNT=$((DURATION * RPS))
fi

log_info "=== Конфигурация ==="
log_info "  API URL: ${API_URL}"
log_info "  Зона: ${ZONE_NAME}"
log_info "  Длительность: ${DURATION}s"
log_info "  RPS: ${RPS}"
log_info "  Всего запросов будет: $((DURATION * RPS))"
log_info "  Будет подготовлено записей: ${RECORDS_COUNT}"
log_info "  Batch размер: ${BATCH_SIZE}"
log_info "  Тип теста: ${TEST_TYPE}"
echo ""

# Функция для проверки готовности API
wait_for_api() {
    log_info "Ожидание готовности API..."
    local max_attempts=30
    local attempt=0

    while [ $attempt -lt $max_attempts ]; do
        if curl -s -f "${API_URL}/status" > /dev/null 2>&1; then
            log_success "API готов к работе"
            return 0
        fi
        attempt=$((attempt + 1))
        sleep 1
    done

    log_error "API не отвечает после ${max_attempts} попыток"
    exit 1
}

# Функция для проверки API ключа
check_api_key() {
    log_info "Проверка API ключа..."
    local response=$(curl -s -X GET "${API_URL}/read/zones" \
        -H "X-API-Key: ${API_KEY}" \
        -w "\n%{http_code}")

    local http_code=$(echo "$response" | tail -n1)

    if [ "$http_code" = "200" ]; then
        log_success "API ключ валиден"
        return 0
    else
        log_error "API ключ невалиден или недостаточно прав"
        return 1
    fi
}

# Функция для получения текущих метрик
get_metrics() {
    local metrics_file="${RESULT_DIR}/metrics_${1}.txt"

    if curl -s -f "http://localhost:8080/metrics" > "$metrics_file" 2>/dev/null; then
        log_success "Метрики сохранены в $metrics_file"
    fi

    if curl -s -f "http://localhost:8080/health" > "${RESULT_DIR}/health_${1}.json" 2>/dev/null; then
        log_success "Health check сохранен в ${RESULT_DIR}/health_${1}.json"
    fi
}

# Функция для запуска нагрузочного теста
run_load_test() {
    local name=$1
    local duration=$2
    local rate=$3
    local targets_file=$4
    local timeout=${5:-60s}

    if [ ! -f "$targets_file" ]; then
        log_error "Targets файл не найден: $targets_file"
        return 1
    fi

    log_test "Запуск теста: $name (${duration}s, ${rate} RPS, таймаут ${timeout})"
    log_info "Targets файл: $targets_file"

    local lines_count=$(wc -l < "$targets_file")
    local requests_count=$((lines_count / 5))
    log_info "Подготовлено уникальных записей: $requests_count"
    log_info "Будет отправлено запросов: $((duration * rate))"

    # Проверяем, хватит ли уникальных записей
    local needed_requests=$((duration * rate))
    if [ $needed_requests -gt $requests_count ]; then
        log_error "НЕ ХВАТАЕТ записей! Нужно: $needed_requests, есть: $requests_count"
        return 1
    fi

    # Создаем лог файл для ошибок
    local error_log="${RESULT_DIR}/${name}_errors.log"
    echo "=== Errors log for test $name at $(date) ===" > "$error_log"

    # Запускаем vegeta
    vegeta attack -duration="${duration}s" \
                  -rate="$rate" \
                  -timeout="$timeout" \
                  -targets="$targets_file" \
                  -output="${RESULT_DIR}/${name}.bin" > "${RESULT_DIR}/${name}_attack.log" 2>&1

    if [ ! -f "${RESULT_DIR}/${name}.bin" ]; then
        log_error "Файл результатов не создан для теста $name"
        return 1
    fi

    # Генерируем отчеты
    vegeta report -type=text "${RESULT_DIR}/${name}.bin" > "${RESULT_DIR}/${name}_report.txt"
    vegeta report -type=json "${RESULT_DIR}/${name}.bin" > "${RESULT_DIR}/${name}_report.json"

    # Логируем все ответы с кодом не 200
    vegeta report -type=json "${RESULT_DIR}/${name}.bin" | \
        jq -r '.status_codes | to_entries[] | select(.key != "200") |
            "\n=== STATUS CODE " + .key + " ===\n" +
            "Count: " + (.value | tostring) + "\n"' \
        >> "$error_log" 2>/dev/null || true

    # Анализируем результаты
    if [ -f "${RESULT_DIR}/${name}_report.json" ]; then
        local success=$(jq -r '.success // 0' "${RESULT_DIR}/${name}_report.json" 2>/dev/null || echo "0")
        local total=$(jq -r '.requests // 0' "${RESULT_DIR}/${name}_report.json" 2>/dev/null || echo "0")
        local avg=$(jq -r '.latencies.mean // 0' "${RESULT_DIR}/${name}_report.json" 2>/dev/null || echo "0")
        local p95=$(jq -r '.latencies["95th"] // 0' "${RESULT_DIR}/${name}_report.json" 2>/dev/null || echo "0")
        local p99=$(jq -r '.latencies["99th"] // 0' "${RESULT_DIR}/${name}_report.json" 2>/dev/null || echo "0")

        avg_ms=$(echo "scale=2; $avg / 1000000" | bc 2>/dev/null || echo "0")
        p95_ms=$(echo "scale=2; $p95 / 1000000" | bc 2>/dev/null || echo "0")
        p99_ms=$(echo "scale=2; $p99 / 1000000" | bc 2>/dev/null || echo "0")

        success_percent=$(echo "scale=2; $success * 100" | bc 2>/dev/null || echo "0")

        # Выводим распределение статусов
        log_info "Распределение статусов:"
        vegeta report -type=json "${RESULT_DIR}/${name}.bin" | jq -r '.status_codes | to_entries[] | "  HTTP " + .key + ": " + (.value | tostring)' 2>/dev/null || true

        log_success "Тест $name завершен: успешно=${success_percent}%, запросов=$total, средняя=${avg_ms}ms, p95=${p95_ms}ms"

        echo "$name: успешно=${success_percent}% ($total запросов), средняя=${avg_ms}ms, p95=${p95_ms}ms, p99=${p99_ms}ms" >> "${RESULT_DIR}/summary.txt"

        return 0
    else
        log_error "Не удалось сгенерировать отчет для теста $name"
        return 1
    fi
}

# Функция для создания зоны
create_zone() {
    local zone_name=$1

    log_info "Создание зоны ${zone_name}..."

    local payload=$(cat <<EOF
{
  "name": "${zone_name}",
  "email": "admin@${zone_name}",
  "ns_ip": "192.168.1.1"
}
EOF
)

    local response=$(curl -s -X POST "${API_URL}/write/zone" \
        -H "Content-Type: application/json" \
        -H "X-API-Key: ${API_KEY}" \
        -d "$payload" \
        -w "\n%{http_code}")

    local http_code=$(echo "$response" | tail -n1)
    local body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "200" ] || [ "$http_code" = "201" ]; then
        log_success "Зона ${zone_name} создана"
        echo "$body" | jq '.' > "${RESULT_DIR}/zone_created.json" 2>/dev/null || true
        return 0
    else
        log_error "Ошибка создания зоны: HTTP $http_code"
        echo "$body" | jq '.' 2>/dev/null || echo "$body"
        return 1
    fi
}

# Функция для удаления зоны
delete_zone() {
    local zone_name=$1

    log_info "Удаление зоны ${zone_name}..."

    local response=$(curl -s -X DELETE "${API_URL}/write/zone/${zone_name}" \
        -H "X-API-Key: ${API_KEY}" \
        -w "\n%{http_code}")

    local http_code=$(echo "$response" | tail -n1)

    if [ "$http_code" = "200" ]; then
        log_success "Зона ${zone_name} удалена"
        return 0
    else
        log_warn "Не удалось удалить зону ${zone_name}: HTTP $http_code"
        return 1
    fi
}

# Функция для подготовки уникальных записей
prepare_records() {
    local count=$1
    local zone_name=$2

    RECORDS_DIR="${RESULT_DIR}/records_$(date +%s)"
    mkdir -p "$RECORDS_DIR"

    TARGETS_FILE="${RESULT_DIR}/targets_add.txt"
    rm -f "$TARGETS_FILE"

    log_info "Подготовка ${count} УНИКАЛЬНЫХ A-записей..."
    log_info "Директория записей: $RECORDS_DIR"
    log_info "Targets файл: $TARGETS_FILE"

    local actual_count=0
    local test_start_time=$(date +%s)

    for rec_num in $(seq 1 $count); do
        local unique_id="${test_start_time}_${rec_num}_${RANDOM}"
        local record_name="host-${unique_id}"

        local first_octet=$(( (rec_num / 65536) % 256 ))
        local second_octet=$(( (rec_num / 256) % 256 ))
        local third_octet=$(( rec_num % 256 ))
        local ip="${first_octet}.${second_octet}.${third_octet}.1"

        if [ "$first_octet" = "0" ] && [ "$second_octet" = "0" ] && [ "$third_octet" = "0" ]; then
            ip="10.${rec_num}.0.1"
        fi

        local payload_file="${RECORDS_DIR}/record_${unique_id}.json"

        cat > "$payload_file" << EOF
{
  "name": "${record_name}",
  "type": "A",
  "value": "${ip}",
  "ttl": 3600
}
EOF

        if [ -f "$payload_file" ]; then
            cat >> "$TARGETS_FILE" << EOF
POST ${API_URL}/write/zone/${zone_name}/record
X-API-Key: ${API_KEY}
Content-Type: application/json
@${payload_file}

EOF
            actual_count=$((actual_count + 1))
        fi
    done

    log_success "Подготовлено ${actual_count} уникальных записей (из запрошенных ${count})"

    if [ ! -f "$TARGETS_FILE" ]; then
        log_error "Targets файл не был создан!"
        return 1
    fi

    echo "$TARGETS_FILE"
}

# Функция для тестирования добавления записей
test_add_records() {
    local zone_name=$1
    local records_count=$2
    local duration=$3
    local rps=$4

    log_info "=== Тест добавления записей ==="

    if ! prepare_records "$records_count" "$zone_name"; then
        log_error "Не удалось подготовить записи"
        return 1
    fi

    run_load_test "add_records" "$duration" "$rps" "$TARGETS_FILE" "90s"
    cleanup_temp_files
}

# Функция для тестирования batch режима
test_batch_mode() {
    local zone_name=$1
    local batch_size=$2

    # Для batch режима: больше запросов, выше нагрузка
    local duration=45
    local rps=30
    local records_count=$((duration * rps))

    log_info "=== Тест batch режима ==="
    log_info "  Длительность: ${duration}s, RPS: ${rps}"
    log_info "  Всего запросов: $((duration * rps))"
    log_info "  Уникальных записей: ${records_count}"

    if ! prepare_records "$records_count" "$zone_name"; then
        log_error "Не удалось подготовить записи"
        return 1
    fi

    run_load_test "batch_mode" "$duration" "$rps" "$TARGETS_FILE" "90s"
    cleanup_temp_files
}

# Функция для тестирования валидации
test_validation() {
    local zone_name=$1

    log_info "=== Тест валидации ==="

    local invalid_dir="${RESULT_DIR}/invalid_records_$(date +%s)"
    mkdir -p "$invalid_dir"

    local targets_file="${RESULT_DIR}/targets_invalid.txt"
    rm -f "$targets_file"

    local timestamp=$(date +%s)

    cat > "${invalid_dir}/record_invalid_ip.json" << EOF
{
  "name": "invalid-ip-${timestamp}",
  "type": "A",
  "value": "300.300.300.300",
  "ttl": 3600
}
EOF

    cat > "${invalid_dir}/record_invalid_name.json" << EOF
{
  "name": "invalid..name-${timestamp}",
  "type": "A",
  "value": "10.0.0.1",
  "ttl": 3600
}
EOF

    cat > "${invalid_dir}/record_invalid_type.json" << EOF
{
  "name": "invalid-type-${timestamp}",
  "type": "INVALID",
  "value": "test",
  "ttl": 3600
}
EOF

    for record_file in "${invalid_dir}"/record_invalid_*.json; do
        if [ -f "$record_file" ]; then
            cat >> "$targets_file" << EOF
POST ${API_URL}/write/zone/${zone_name}/record
X-API-Key: ${API_KEY}
Content-Type: application/json
@${record_file}

EOF
        fi
    done

    log_test "Отправка невалидных запросов..."

    local total=0
    local failed=0

    while IFS= read -r line; do
        if [[ "$line" == POST* ]]; then
            total=$((total + 1))
            local payload_file=$(echo "$line" | grep -o '@[^ ]*' | sed 's/@//')
            if [ -f "$payload_file" ]; then
                local http_code=$(curl -s -X POST "${API_URL}/write/zone/${zone_name}/record" \
                    -H "Content-Type: application/json" \
                    -H "X-API-Key: ${API_KEY}" \
                    -d @"$payload_file" \
                    -w "%{http_code}" -o /dev/null)

                if [ "$http_code" = "400" ] || [ "$http_code" = "422" ]; then
                    log_success "Корректно отклонен (HTTP $http_code)"
                else
                    log_error "Не отклонен (HTTP $http_code)"
                    failed=$((failed + 1))
                fi
            fi
        fi
    done < "$targets_file"

    if [ $failed -eq 0 ]; then
        log_success "Валидация работает корректно: все $total запросов отклонены"
    else
        log_error "Валидация работает некорректно: $failed из $total не отклонены"
    fi

    rm -rf "$invalid_dir"
    rm -f "$targets_file"
}

# Глобальные переменные для очистки
TARGETS_FILE=""
RECORDS_DIR=""

cleanup_temp_files() {
    if [ -n "$TARGETS_FILE" ] && [ -f "$TARGETS_FILE" ]; then
        rm -f "$TARGETS_FILE"
    fi
    if [ -n "$RECORDS_DIR" ] && [ -d "$RECORDS_DIR" ]; then
        rm -rf "$RECORDS_DIR"
    fi
}

# Основная функция
main() {
    wait_for_api

    if ! check_api_key; then
        exit 1
    fi

    get_metrics "initial"

    if ! create_zone "$ZONE_NAME"; then
        log_error "Не удалось создать зону"
        exit 1
    fi

    get_metrics "after_zone_creation"

    case $TEST_TYPE in
        "add_records")
            test_add_records "$ZONE_NAME" "$RECORDS_COUNT" "$DURATION" "$RPS"
            ;;
        "batch")
            test_batch_mode "$ZONE_NAME" "$BATCH_SIZE"
            ;;
        "validation")
            test_validation "$ZONE_NAME"
            ;;
        *)
            log_error "Неизвестный тип теста: ${TEST_TYPE}"
            exit 1
            ;;
    esac

    get_metrics "final"

    echo ""
    log_info "=== Сводка результатов ==="
    if [ -f "${RESULT_DIR}/summary.txt" ]; then
        cat "${RESULT_DIR}/summary.txt"
    fi

    if [ "${CLEANUP}" = "true" ]; then
        delete_zone "$ZONE_NAME"
    fi

    log_success "=== Тестирование завершено ==="
    log_info "Результаты: ${RESULT_DIR}"
}

trap 'cleanup_temp_files; exit 1' INT TERM
main "$@"