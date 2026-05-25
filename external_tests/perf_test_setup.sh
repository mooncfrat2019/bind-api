#!/bin/bash
# Нагрузочное тестирование BIND Manager API

# ========== КОНФИГУРАЦИЯ ПО УМОЛЧАНИЮ ==========
API_URL="${API_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-}"
ZONE_NAME="${ZONE_NAME:-perf-test.local}"
RECORD_COUNT="${RECORD_COUNT:-2000}"
CONCURRENT_JOBS="${CONCURRENT_JOBS:-5}"
TIMEOUT="${TIMEOUT:-300}"
MODE="${MODE:-interactive}"
DELETE_COUNT="${DELETE_COUNT:-100}"           # Количество записей для удаления
DELETE_RANDOM="${DELETE_RANDOM:-true}"        # Случайное удаление (true/false)

# Директория для логов
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_DIR="${SCRIPT_DIR}/logs"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")

# Файлы логов
ERROR_LOG="${LOG_DIR}/errors_${TIMESTAMP}.log"
SUCCESS_LOG="${LOG_DIR}/success_${TIMESTAMP}.log"
DETAILED_LOG="${LOG_DIR}/detailed_${TIMESTAMP}.log"
RESPONSE_LOG="${LOG_DIR}/responses_${TIMESTAMP}.log"
QUEUE_LOG="${LOG_DIR}/queue_${TIMESTAMP}.log"
DELETE_LOG="${LOG_DIR}/deleted_${TIMESTAMP}.log"

# Цвета
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
NC='\033[0m'

# ========== ФУНКЦИИ ==========
print_help() {
    echo "BIND API Performance Test Tool"
    echo ""
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -u, --url URL          API URL (default: http://localhost:8080)"
    echo "  -k, --key KEY          API Key (required)"
    echo "  -z, --zone NAME        Zone name (default: perf-test.local)"
    echo "  -c, --count NUM        Number of records to create (default: 2000)"
    echo "  -j, --jobs NUM         Concurrent jobs (default: 5)"
    echo "  -m, --mode MODE        Mode: interactive, sequential, parallel (default: interactive)"
    echo "  -d, --delete NUM       Number of records to delete (default: 100)"
    echo "  -r, --random           Delete random records (default: true)"
    echo "  -s, --sequential-del   Delete sequential records"
    echo "  -h, --help             Show this help"
    echo ""
    echo "Examples:"
    echo "  $0 -k your-key -c 2000 -d 100                    # Создать 2000, удалить 100 случайных"
    echo "  $0 -k your-key -c 2000 -d 50 -s                  # Создать 2000, удалить первые 50"
    echo "  $0 -k your-key -m parallel -j 10 -c 5000 -d 500  # Создать 5000, удалить 500 случайных"
}

log_info() {
    local msg="[INFO] $1"
    echo -e "${BLUE}${msg}${NC}"
    echo "$(date '+%Y-%m-%d %H:%M:%S') ${msg}" >> "${DETAILED_LOG}"
}

log_success() {
    local msg="[SUCCESS] $1"
    echo -e "${GREEN}${msg}${NC}"
    echo "$(date '+%Y-%m-%d %H:%M:%S') ${msg}" >> "${DETAILED_LOG}"
}

log_warn() {
    local msg="[WARN] $1"
    echo -e "${YELLOW}${msg}${NC}"
    echo "$(date '+%Y-%m-%d %H:%M:%S') ${msg}" >> "${DETAILED_LOG}"
}

log_error() {
    local msg="[ERROR] $1"
    echo -e "${RED}${msg}${NC}"
    echo "$(date '+%Y-%m-%d %H:%M:%S') ${msg}" >> "${DETAILED_LOG}"
}

log_delete() {
    local msg="[DELETE] $1"
    echo -e "${MAGENTA}${msg}${NC}"
    echo "$(date '+%Y-%m-%d %H:%M:%S') ${msg}" >> "${DELETE_LOG}"
}

# Генерация IP
generate_ip() {
    local idx=$1
    local offset=$((idx - 1))
    local octet3=$((offset / 256))
    local octet4=$((offset % 256))
    echo "10.10.${octet3}.${octet4}"
}

# Проверка API
check_api() {
    log_info "Проверка доступности API..."

    local http_code
    http_code=$(curl -s -o /dev/null -w "%{http_code}" "${API_URL}/api/status" 2>/dev/null)

    if [ "$http_code" = "200" ]; then
        log_success "API доступен"
        return 0
    else
        log_error "API недоступен (HTTP $http_code)"
        return 1
    fi
}

# Проверка BIND
check_bind() {
    log_info "Проверка статуса BIND..."

    local response
    response=$(curl -s "${API_URL}/api/status" 2>/dev/null)

    local status
    status=$(echo "$response" | jq -r '.data.named_status // empty' 2>/dev/null)

    if [ "$status" = "active" ]; then
        log_success "BIND активен"
        return 0
    else
        log_warn "BIND статус: ${status:-неизвестен}"
        return 0
    fi
}

# Создание зоны
create_zone() {
    log_info "Создание тестовой зоны ${ZONE_NAME}..."

    local response
    response=$(curl -s -X POST "${API_URL}/api/write/zone" \
        -H "Content-Type: application/json" \
        -H "X-API-Key: ${API_KEY}" \
        -d "{
            \"name\": \"${ZONE_NAME}\",
            \"email\": \"admin.${ZONE_NAME}\",
            \"ns_ip\": \"10.69.13.3\"
        }" 2>/dev/null)

    local success
    success=$(echo "$response" | jq -r '.success // false' 2>/dev/null)
    local message
    message=$(echo "$response" | jq -r '.message // "Unknown error"' 2>/dev/null)

    if [ "$success" = "true" ]; then
        log_success "Зона ${ZONE_NAME} создана"
        return 0
    else
        log_error "Ошибка создания зоны: ${message}"
        return 1
    fi
}

# Добавление одной A записи
add_record() {
    local i=$1
    local name="host-${i}"
    local ip=$(generate_ip $i)

    local response
    response=$(curl -s -X POST "${API_URL}/api/write/zone/${ZONE_NAME}/record" \
        -H "Content-Type: application/json" \
        -H "X-API-Key: ${API_KEY}" \
        -d "{
            \"name\": \"${name}\",
            \"type\": \"A\",
            \"value\": \"${ip}\",
            \"ttl\": 300
        }" 2>/dev/null)

    local success
    success=$(echo "$response" | jq -r '.success // false' 2>/dev/null)

    if [ "$success" = "true" ]; then
        return 0
    else
        return 1
    fi
}

# Удаление записи
delete_record() {
    local name=$1

    local response
    response=$(curl -s -X DELETE "${API_URL}/api/write/zone/${ZONE_NAME}/record/${name}/A" \
        -H "X-API-Key: ${API_KEY}" 2>/dev/null)

    local success
    success=$(echo "$response" | jq -r '.success // false' 2>/dev/null)

    if [ "$success" = "true" ]; then
        return 0
    else
        return 1
    fi
}

# Генерация случайных индексов для удаления
generate_random_indices() {
    local count=$1
    local max=$2
    shuf -i 1-${max} -n ${count} 2>/dev/null || jot -r ${count} 1 ${max}
}

# Последовательное добавление
run_sequential_create() {
    log_info "=== СОЗДАНИЕ ЗАПИСЕЙ (ПОСЛЕДОВАТЕЛЬНО) ==="
    log_info "Добавление ${RECORD_COUNT} записей..."

    local start_time=$(date +%s)
    local success=0
    local failed=0

    for i in $(seq 1 $RECORD_COUNT); do
        if add_record $i; then
            success=$((success + 1))
        else
            failed=$((failed + 1))
        fi

        if [ $((i % 100)) -eq 0 ]; then
            log_info "Прогресс создания: ${i}/${RECORD_COUNT} (успешно: ${success})"
        fi
    done

    local end_time=$(date +%s)
    local total_time=$((end_time - start_time))

    log_info "========== РЕЗУЛЬТАТЫ СОЗДАНИЯ =========="
    log_info "Всего: ${RECORD_COUNT}"
    log_info "Успешно: ${success}"
    log_info "Ошибок: ${failed}"
    log_info "Время: ${total_time} сек"
    if [ $total_time -gt 0 ]; then
        log_info "Скорость: $((success / total_time)) зап/сек"
    fi
}

# Параллельное добавление
run_parallel_create() {
    log_info "=== СОЗДАНИЕ ЗАПИСЕЙ (ПАРАЛЛЕЛЬНО) ==="
    log_info "Добавление ${RECORD_COUNT} записей (${CONCURRENT_JOBS} потоков)..."

    local start_time=$(date +%s)
    local temp_dir=$(mktemp -d /tmp/bind-test-XXXXXX)
    local success_file="${temp_dir}/success.txt"
    local running=0
    local i=1

    > "${success_file}"

    while [ $i -le $RECORD_COUNT ]; do
        while [ $running -lt $CONCURRENT_JOBS ] && [ $i -le $RECORD_COUNT ]; do
            (
                if add_record $i; then
                    echo "1" >> "${success_file}"
                fi
            ) &
            running=$((running + 1))
            i=$((i + 1))
        done

        wait -n 2>/dev/null || true
        running=$((running - 1))

        local current_success=$(wc -l < "${success_file}" 2>/dev/null | tr -d ' ')
        current_success=${current_success:-0}

        if [ $((current_success % 100)) -eq 0 ] && [ $current_success -gt 0 ]; then
            printf "\rПрогресс создания: ${current_success}/${RECORD_COUNT}"
        fi
    done

    wait

    local end_time=$(date +%s)
    local total_time=$((end_time - start_time))
    local success=$(wc -l < "${success_file}" 2>/dev/null | tr -d ' ')
    success=${success:-0}

    echo ""
    log_info "========== РЕЗУЛЬТАТЫ СОЗДАНИЯ =========="
    log_info "Всего: ${RECORD_COUNT}"
    log_info "Успешно: ${success}"
    log_info "Ошибок: $((RECORD_COUNT - success))"
    log_info "Время: ${total_time} сек"
    if [ $total_time -gt 0 ] && [ $success -gt 0 ]; then
        log_info "Скорость: $((success / total_time)) зап/сек"
    fi

    rm -rf "${temp_dir}"
}

# Удаление записей
run_delete() {
    local delete_mode=$1
    local delete_list=$2

    log_info "=== УДАЛЕНИЕ ЗАПИСЕЙ ==="

    local delete_indices=()

    if [ "$delete_mode" = "random" ]; then
        log_info "Генерация ${DELETE_COUNT} случайных индексов для удаления..."
        delete_indices=($(generate_random_indices $DELETE_COUNT $RECORD_COUNT))
    else
        log_info "Удаление первых ${DELETE_COUNT} записей..."
        for i in $(seq 1 $DELETE_COUNT); do
            delete_indices+=($i)
        done
    fi

    local start_time=$(date +%s)
    local success=0
    local failed=0

    for idx in "${delete_indices[@]}"; do
        local name="host-${idx}"
        local ip=$(generate_ip $idx)

        if delete_record "$name"; then
            success=$((success + 1))
            log_delete "Удалена запись ${name} -> ${ip}"
        else
            failed=$((failed + 1))
            log_warn "Ошибка удаления записи ${name}"
        fi
    done

    local end_time=$(date +%s)
    local total_time=$((end_time - start_time))

    log_info "========== РЕЗУЛЬТАТЫ УДАЛЕНИЯ =========="
    log_info "Всего для удаления: ${DELETE_COUNT}"
    log_info "Успешно удалено: ${success}"
    log_info "Ошибок удаления: ${failed}"
    log_info "Время: ${total_time} сек"
    if [ $total_time -gt 0 ]; then
        log_info "Скорость: $((success / total_time)) удалений/сек"
    fi
}

# Проверка DNS резолвинга после операций
test_resolution() {
    log_info "=== ПРОВЕРКА DNS РЕЗОЛВИНГА ==="

    local test_ip="${REPLICA_IP:-100.69.13.4}"
    local created_count=0
    local deleted_count=0

    # Проверяем все записи
    for i in $(seq 1 $RECORD_COUNT); do
        local name="host-${i}.${ZONE_NAME}"
        local resolved_ip
        resolved_ip=$(dig +short "${name}" @"${test_ip}" | head -1)

        if [ -n "$resolved_ip" ]; then
            created_count=$((created_count + 1))
        else
            deleted_count=$((deleted_count + 1))
        fi
    done

    log_info "========== РЕЗУЛЬТАТЫ ПРОВЕРКИ DNS =========="
    log_info "Всего записей в зоне: ${RECORD_COUNT}"
    log_info "Разрешаются успешно: ${created_count}"
    log_info "Не разрешаются (удалены или не созданы): ${deleted_count}"
}

# Очистка
cleanup() {
    log_info "Очистка тестовых данных..."

    curl -s -X DELETE "${API_URL}/api/write/zone/${ZONE_NAME}" \
        -H "X-API-Key: ${API_KEY}" > /dev/null 2>&1

    log_success "Очистка завершена"
}

# ========== ОБРАБОТКА АРГУМЕНТОВ ==========
while [[ $# -gt 0 ]]; do
    case $1 in
        -u|--url)
            API_URL="$2"
            shift 2
            ;;
        -k|--key)
            API_KEY="$2"
            shift 2
            ;;
        -z|--zone)
            ZONE_NAME="$2"
            shift 2
            ;;
        -c|--count)
            RECORD_COUNT="$2"
            shift 2
            ;;
        -j|--jobs)
            CONCURRENT_JOBS="$2"
            shift 2
            ;;
        -m|--mode)
            MODE="$2"
            shift 2
            ;;
        -d|--delete)
            DELETE_COUNT="$2"
            shift 2
            ;;
        -r|--random)
            DELETE_RANDOM="true"
            shift
            ;;
        -s|--sequential-del)
            DELETE_RANDOM="false"
            shift
            ;;
        -h|--help)
            print_help
            exit 0
            ;;
        *)
            echo "Неизвестный параметр: $1"
            print_help
            exit 1
            ;;
    esac
done

# ========== MAIN ==========
main() {
    if [ -z "$API_KEY" ]; then
        echo -e "${RED}[ERROR] API Key не указан${NC}"
        print_help
        exit 1
    fi

    mkdir -p "${LOG_DIR}"

    log_info "========== BIND API PERFORMANCE TEST =========="
    log_info "API URL: ${API_URL}"
    log_info "Zone: ${ZONE_NAME}"
    log_info "Records to create: ${RECORD_COUNT}"
    log_info "Records to delete: ${DELETE_COUNT}"
    log_info "Delete mode: $([ "$DELETE_RANDOM" = "true" ] && echo "random" || echo "sequential")"
    log_info "Mode: ${MODE}"
    if [ "$MODE" = "parallel" ]; then
        log_info "Threads: ${CONCURRENT_JOBS}"
    fi
    log_info "Logs dir: ${LOG_DIR}"
    echo ""

    # Проверка зависимостей
    for cmd in curl jq dig; do
        if ! command -v $cmd &> /dev/null; then
            log_error "Команда $cmd не найдена"
            exit 1
        fi
    done

    # Проверка API
    if ! check_api; then
        exit 1
    fi

    check_bind

    # Создание зоны
    if ! create_zone; then
        log_error "Не удалось создать зону"
        exit 1
    fi

    log_info "Ожидание применения конфигурации (3 сек)..."
    sleep 3

    # Выбор режима
    if [ "$MODE" = "interactive" ]; then
        echo ""
        echo "Выберите действие:"
        echo "  1) Только создание записей"
        echo "  2) Создание + удаление записей"
        echo "  3) Только удаление (если записи уже созданы)"
        read -p "Ваш выбор [1-3]: " action_choice

        echo ""
        echo "Выберите режим выполнения:"
        echo "  1) Последовательный"
        echo "  2) Параллельный (${CONCURRENT_JOBS} потоков)"
        read -p "Ваш выбор [1-2]: " mode_choice

        EXEC_MODE="sequential"
        if [ "$mode_choice" = "2" ]; then
            EXEC_MODE="parallel"
        fi
    else
        action_choice="2"
        EXEC_MODE="$MODE"
    fi

    # Создание записей
    if [ "$action_choice" = "1" ] || [ "$action_choice" = "2" ]; then
        if [ "$EXEC_MODE" = "sequential" ]; then
            run_sequential_create
        else
            run_parallel_create
        fi
    fi

    # Удаление записей
    if [ "$action_choice" = "2" ]; then
        # Ждём обработки очереди
        log_info "Ожидание обработки очереди (10 сек)..."
        sleep 10

        if [ "$DELETE_RANDOM" = "true" ]; then
            run_delete "random"
        else
            run_delete "sequential"
        fi
    elif [ "$action_choice" = "3" ]; then
        if [ "$DELETE_RANDOM" = "true" ]; then
            run_delete "random"
        else
            run_delete "sequential"
        fi
    fi

    # Проверка DNS
    echo ""
    read -p "Проверить DNS резолвинг? (y/n): " check_dns
    if [ "$check_dns" = "y" ] || [ "$check_dns" = "Y" ]; then
        test_resolution
    fi

    # Очистка
    echo ""
    read -p "Удалить тестовую зону? (y/n): " do_cleanup
    if [ "$do_cleanup" = "y" ] || [ "$do_cleanup" = "Y" ]; then
        cleanup
    fi

    log_success "========== ТЕСТ ЗАВЕРШЁН =========="
    log_info "Логи сохранены в: ${LOG_DIR}"
}

trap cleanup INT

main "$@"