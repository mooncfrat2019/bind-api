#!/bin/bash
# test/load/scripts/cleanup.sh

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

log_info "=== Очистка тестовых данных ==="

# Очистка временных файлов
rm -f /tmp/targets_*.txt 2>/dev/null || true
rm -f /app/.api_key 2>/dev/null || true

# Очистка результатов
rm -rf /app/results/* 2>/dev/null || true
rm -rf /app/reports/* 2>/dev/null || true

# Очистка тестовых данных
rm -rf /app/data/payloads/*.json 2>/dev/null || true
rm -rf /app/data/payloads/*.jsonl 2>/dev/null || true

log_info "Очистка завершена"