#!/bin/bash
set -e

echo "========================================="
echo "Starting BIND DNS Server"
echo "========================================="

# Проверяем наличие конфигурационных файлов
if [ ! -f /etc/named.conf ]; then
    echo "ERROR: /etc/named.conf not found!"
    exit 1
fi

# Создаем необходимые директории
mkdir -p /var/named/data /var/named/slaves /var/named/dynamic
chown -R named:named /var/named

# Проверяем конфигурацию BIND
echo "Checking BIND configuration..."
named-checkconf /etc/named.conf
if [ $? -ne 0 ]; then
    echo "ERROR: BIND configuration check failed!"
    exit 1
fi

# Запускаем named в фоне
echo "Starting named process..."
named -g -u named &
NAMED_PID=$!

# Ждем запуска BIND
echo "Waiting for BIND to start..."
sleep 5

# Проверяем статус несколько раз
MAX_RETRIES=10
RETRY_COUNT=0

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if pgrep -x named > /dev/null; then
        echo "✓ BIND named started successfully (PID: $(pgrep -x named))"

        # Проверяем, слушает ли порт 53
        if netstat -uln 2>/dev/null | grep -q ":53 " || ss -uln 2>/dev/null | grep -q ":53 "; then
            echo "✓ BIND is listening on port 53"
        else
            echo "⚠ BIND is not listening on port 53 yet"
        fi
        break
    fi

    RETRY_COUNT=$((RETRY_COUNT + 1))
    echo "Waiting for BIND to start... (attempt $RETRY_COUNT/$MAX_RETRIES)"
    sleep 2
done

if ! pgrep -x named > /dev/null; then
    echo "⚠ WARNING: BIND named failed to start or not detected"
    echo "Check logs for more information"
fi

echo "========================================="
echo "Starting BIND API Application"
echo "========================================="

# Запускаем приложение
exec "$@"