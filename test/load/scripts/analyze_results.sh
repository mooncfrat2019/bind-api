#!/bin/bash

RESULT_DIR="${1:-/app/results}"
LATEST_DIR=$(ls -td "$RESULT_DIR"/*/ | head -1)

echo "=== Анализ результатов тестирования ==="
echo "Директория: $LATEST_DIR"
echo ""

# Анализ каждого отчета
for report in "$LATEST_DIR"/*_report.json; do
    if [ -f "$report" ]; then
        name=$(basename "$report" _report.json)
        echo "--- $name ---"

        success=$(jq -r '.success' "$report")
        requests=$(jq -r '.requests' "$report")
        avg=$(jq -r '.latencies.mean' "$report")
        p50=$(jq -r '.latencies["50th"]' "$report")
        p95=$(jq -r '.latencies["95th"]' "$report")
        p99=$(jq -r '.latencies["99th"]' "$report")
        max=$(jq -r '.latencies.max' "$report")

        echo "  Успешность: $(echo "$success * 100" | bc -l)%"
        echo "  Запросов: $requests"
        echo "  Средняя задержка: $avg мс"
        echo "  P50: $p50 мс"
        echo "  P95: $p95 мс"
        echo "  P99: $p99 мс"
        echo "  Максимум: $max мс"
        echo ""
    fi
done

# Анализ метрик
if [ -f "$LATEST_DIR/metrics_final.txt" ]; then
    echo "--- Метрики системы ---"
    grep -E "bind_(queue|operations|http)" "$LATEST_DIR/metrics_final.txt" | head -20
fi

echo "=== Анализ завершен ==="