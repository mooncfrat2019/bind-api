# Инструкция по работе с BIND API

## Оглавление

1. [Введение](#1-введение)
2. [Подготовка и настройка](#2-подготовка-и-настройка)
3. [Аутентификация и API-ключи](#3-аутентификация-и-api-ключи)
4. [Работа с DNS-зонами](#4-работа-с-dns-зонами)
5. [Работа с DNS-записями](#5-работа-с-dns-записями)
6. [Мониторинг и аудит](#6-мониторинг-и-аудит)
7. [Управление API-ключами](#7-управление-api-ключами)
8. [Работа с репликами](#8-работа-с-репликами)
9. [Практические сценарии](#9-практические-сценарии)
10. [Обработка ошибок](#10-обработка-ошибок)
11. [Примеры скриптов](#11-примеры-скриптов)

---

## 1. Введение

BIND Manager API — это REST API для управления DNS-сервером BIND. Он поддерживает архитектуру Master-Replica, автоматическую синхронизацию, версионирование конфигураций и систему API-ключей.

**Базовый URL:** `http://<server-ip>:8080/api`  
**Формат данных:** JSON  
**Кодировка:** UTF-8

### 1.1. Общая структура ответа

Все API-ответы имеют единую структуру:

```json
{
  "success": true,
  "message": "Описание результата",
  "data": {}
}
```

- `success` — статус операции (`true`/`false`)
- `message` — текстовое описание
- `data` — полезные данные (может отсутствовать)

---

## 2. Подготовка и настройка

### 2.1. Получение API-ключа

#### Bootstrap API-ключ (первый запуск)

При первом запуске MASTER можно создать временный bootstrap-ключ из переменной окружения `BIND_API_BOOTSTRAP_KEY`.

**Условия создания bootstrap-ключа:**
- таблица `api_keys` пуста;
- переменная `BIND_API_BOOTSTRAP_KEY` задана;
- значение ключа имеет допустимую длину (32-120 символов).

**Особенности bootstrap-ключа:**
- права доступа: `*`
- срок действия: **7 дней**
- значение ключа **не выводится в лог**

**Пример запуска:**
```shell script
export BIND_API_BOOTSTRAP_KEY="your-very-secure-bootstrap-key-value-min-32-chars"
./bind-api
```

> **Важно:** После первого входа создайте постоянный API-ключ через `/api/keys`, затем отзовите bootstrap-ключ.

#### Создание постоянного ключа

```shell script
curl -X POST http://localhost:8080/api/keys \
  -H "Content-Type: application/json" \
  -H "X-API-Key: <bootstrap-key>" \
  -d '{
    "name": "admin-key",
    "description": "Постоянный административный ключ",
    "permissions": ["*"],
    "expires_in": 0
  }'
```


### 2.2. Проверка доступности API

```shell script
curl http://localhost:8080/api/status
```

**Ответ:**
```json
{
  "success": true,
  "message": "Статус сервиса",
  "data": {
    "named_status": "active",
    "api_version": "1.0.0",
    "role": "master",
    "db_connected": true,
    "queue_size": 0
  }
}
```

---

## 3. Аутентификация и API-ключи

### 3.1. Формат авторизации

API-ключ передаётся в заголовке `X-API-Key`:

```shell
curl -H "X-API-Key: <ваш_ключ>" http://localhost:8080/api/read/zones
```

### 3.2. Права доступа

| Право | Эндпоинты | Описание |
|-------|-----------|----------|
| `zone:read` | `/read/*` | Чтение зон, конфигов, аудита |
| `zone:write` | `/write/*` | Создание, изменение, удаление зон и записей |
| `admin` | `/keys/*` | Управление API-ключами |
| `*` | Все эндпоинты | Полный доступ |

### 3.3. Создание нового API-ключа

**Требуемое право:** `admin`

```shell
curl -X POST http://localhost:8080/api/keys \
  -H "Content-Type: application/json" \
  -H "X-API-Key: <admin_ключ>" \
  -d '{
    "name": "monitoring-service",
    "description": "Ключ для системы мониторинга",
    "permissions": ["zone:read"],
    "ip_address": "10.69.13.100",
    "expires_in": 90
  }'
```

**Параметры:**

| Поле | Тип | Обязательное | Описание |
|------|-----|--------------|----------|
| `name` | string | Да | Название ключа (3-100 символов) |
| `description` | string | Нет | Описание назначения |
| `permissions` | array | Да | Массив прав (`zone:read`, `zone:write`, `admin`, `*`) |
| `ip_address` | string | Нет | Привязка к IP (пусто = любой IP) |
| `expires_in` | int | Нет | Срок действия в днях (0 = бессрочный) |

### 3.4. Просмотр всех ключей

```shell
curl -H "X-API-Key: <admin_ключ>" http://localhost:8080/api/keys
```

### 3.5. Отзыв ключа

```shell 
curl -X DELETE http://localhost:8080/api/keys/5 \
  -H "X-API-Key: <admin_ключ>"
```

> **Примечание:** Нельзя отозвать ключ, который используется для текущего запроса.

### 3.6. Защита от brute-force

Система автоматически защищает API от подбора ключей:

- После **5 неудачных попыток** авторизации IP блокируется на **15 минут**
- Автоматическая очистка старых попыток (каждые 5 минут)
- Блокировка логируется: `journalctl -u bind-api | grep "заблокирован"`

**Пример лога блокировки:**
```
[WARN] IP 192.168.1.100 заблокирован на 15 минут после 5 неудачных попыток
```

**Что делать если IP заблокирован:**
1. Подождать 15 минут (автоматическая разблокировка)
2. Или перезапустить сервис: `sudo systemctl restart bind-api`

---

## 4. Работа с DNS-зонами

### 4.1. Просмотр списка всех зон

**Требуемое право:** `zone:read`

```shell
curl -H "X-API-Key: <ключ>" http://localhost:8080/api/read/zones
```

**Ответ:**
```json
{
  "success": true,
  "message": "Список зон",
  "data": {
    "zones": [
      {
        "name": "example.local",
        "file": "/var/named/example.local.zone",
        "type": "forward",
        "config_file": "/etc/named.zones.conf",
        "record_count": 4
      }
    ]
  }
}
```

### 4.2. Просмотр информации о конкретной зоне

```shell
curl -H "X-API-Key: <ключ>" http://localhost:8080/api/read/zone/example.local
```

**Ответ:**
```json
{
  "success": true,
  "message": "Информация о зоне",
  "data": {
    "name": "example.local",
    "type": "forward",
    "file": "/var/named/example.local.zone",
    "config_file": "/etc/named.zones.conf",
    "record_count": 4,
    "records": [
      {"name": "@", "type": "SOA", "ttl": 3600, "value": "ns1.example.local. admin.example.local. 2026052701 3600 600 604800 3600"},
      {"name": "@", "type": "NS", "ttl": 3600, "value": "ns1.example.local."},
      {"name": "@", "type": "A", "ttl": 3600, "value": "192.168.1.1"},
      {"name": "www", "type": "A", "ttl": 3600, "value": "192.168.1.10"}
    ]
  }
}
```

### 4.3. Создание новой зоны

**Требуемое право:** `zone:write`

```shell
curl -X POST http://localhost:8080/api/write/zone \
  -H "Content-Type: application/json" \
  -H "X-API-Key: <ключ>" \
  -d '{
    "name": "mycompany.local",
    "email": "admin.mycompany.local",
    "ns_ip": "192.168.1.1"
  }'
```

**Параметры:**

| Поле | Тип | Обязательное | Описание |
|------|-----|--------------|----------|
| `name` | string | Да | Имя зоны (FQDN) |
| `email` | string | Нет | Email администратора (для SOA) |
| `ns_ip` | string | Нет | IP NS-сервера |
| `config_file` | string | Нет | Целевой файл конфигурации |

**Создаваемая зона будет содержать:**
- SOA-запись с автоматическим серийным номером
- NS-запись
- A-запись для @
- A-запись для ns1

### 4.4. Удаление зоны

**Требуемое право:** `zone:write`

```shell
curl -X DELETE http://localhost:8080/api/write/zone/mycompany.local \
  -H "X-API-Key: <ключ>"
```

> **Внимание:** Удаление зоны физически удаляет файл зоны и записи из конфигурации.

---

## 5. Работа с DNS-записями

### 5.1. Добавление записи

**Требуемое право:** `zone:write`

```shell
curl -X POST http://localhost:8080/api/write/zone/mycompany.local/record \
  -H "Content-Type: application/json" \
  -H "X-API-Key: <ключ>" \
  -d '{
    "name": "www",
    "type": "A",
    "value": "192.168.1.100",
    "ttl": 3600
  }'
```

**Параметры:**

| Поле | Тип | Обязательное | Описание |
|------|-----|--------------|----------|
| `name` | string | Да | Имя записи (без домена) |
| `type` | string | Да | Тип записи (A, AAAA, CNAME, MX, TXT, NS) |
| `value` | string | Да | Значение записи |
| `ttl` | int | Нет | TTL в секундах (по умолчанию 3600) |
| `reverse_ptr` | string | Нет | PTR-запись для обратной зоны |

### 5.2. Примеры добавления разных типов записей

#### A-запись (IPv4)

```shell
curl -X POST http://localhost:8080/api/write/zone/example.local/record \
  -H "Content-Type: application/json" -H "X-API-Key: <ключ>" \
  -d '{"name": "www", "type": "A", "value": "10.10.10.100"}'
```

#### AAAA-запись (IPv6)

```shell 
curl -X POST http://localhost:8080/api/write/zone/example.local/record \
  -H "Content-Type: application/json" -H "X-API-Key: <ключ>" \
  -d '{"name": "ipv6", "type": "AAAA", "value": "2001:0db8:85a3:0000:0000:8a2e:0370:7334"}'
```

#### CNAME-запись

```shell
curl -X POST http://localhost:8080/api/write/zone/example.local/record \
  -H "Content-Type: application/json" -H "X-API-Key: <ключ>" \
  -d '{"name": "mail", "type": "CNAME", "value": "www.example.local."}'
```

#### MX-запись

```shell
curl -X POST http://localhost:8080/api/write/zone/example.local/record \
  -H "Content-Type: application/json" -H "X-API-Key: <ключ>" \
  -d '{"name": "@", "type": "MX", "value": "10 mail.example.local."}'
```

#### TXT-запись

```shell
curl -X POST http://localhost:8080/api/write/zone/example.local/record \
  -H "Content-Type: application/json" -H "X-API-Key: <ключ>" \
  -d '{"name": "@", "type": "TXT", "value": "v=spf1 mx ~all"}'
```

### 5.3. Удаление записи

**Требуемое право:** `zone:write`

```shell
curl -X DELETE "http://localhost:8080/api/write/zone/example.local/record/www/A" \
  -H "X-API-Key: <ключ>"
```

**Формат пути:** `/write/zone/{зона}/record/{имя_записи}/{тип}`

### 5.4. Перезагрузка BIND

**Требуемое право:** `zone:write`

```shell
curl -X POST http://localhost:8080/api/write/reload \
  -H "X-API-Key: <ключ>"
```

> После добавления/удаления записей BIND перезагружается автоматически.

---

## 6. Мониторинг и аудит

### 6.1. Просмотр журнала аудита

**Требуемое право:** `zone:read`

```shell
# Все записи
curl -H "X-API-Key: <ключ>" http://localhost:8080/api/read/audit

# Фильтр по зоне
curl -H "X-API-Key: <ключ>" "http://localhost:8080/api/read/audit?zone=example.local"

# Фильтр по статусу
curl -H "X-API-Key: <ключ>" "http://localhost:8080/api/read/audit?status=FAILED"

# Фильтр по типу задания
curl -H "X-API-Key: <ключ>" "http://localhost:8080/api/read/audit?job_type=ADD_RECORD"
```

**Ответ:**
```json
{
  "success": true,
  "message": "Журнал аудита",
  "data": {
    "logs": [
      {
        "id": 123,
        "job_type": "ADD_RECORD",
        "zone_name": "example.local",
        "record_name": "www",
        "record_type": "A",
        "status": "COMPLETED",
        "error": "",
        "created_at": "2026-05-27T10:30:00Z",
        "completed_at": "2026-05-27T10:30:01Z"
      }
    ]
  }
}
```

### 6.2. Статистика аудита

```shell
curl -H "X-API-Key: <ключ>" http://localhost:8080/api/read/audit/stats
```

**Ответ:**
```json
{
  "success": true,
  "message": "Статистика аудита",
  "data": {
    "total": 1250,
    "completed": 1240,
    "failed": 10,
    "success_rate": 99.2
  }
}
```

### 6.3. Просмотр конфигурации API

```shell
curl -H "X-API-Key: <ключ>" http://localhost:8080/api/read/config
```

---

## 7. Управление API-ключами

### 7.1. Создание ключа с ограничениями

```shell
# Ключ только для чтения, с привязкой к IP и сроком 30 дней
curl -X POST http://localhost:8080/api/keys \
  -H "Content-Type: application/json" \
  -H "X-API-Key: <admin_ключ>" \
  -d '{
    "name": "readonly-key",
    "description": "Только чтение зон",
    "permissions": ["zone:read"],
    "ip_address": "10.69.13.100",
    "expires_in": 30
  }'
```

### 7.2. Создание ключа с полным доступом

```shell
curl -X POST http://localhost:8080/api/keys \
  -H "Content-Type: application/json" \
  -H "X-API-Key: <admin_ключ>" \
  -d '{
    "name": "full-access-key",
    "permissions": ["*"],
    "expires_in": 0
  }'
```

### 7.3. Просмотр и отзыв ключей

```shell
# Список ключей
curl -H "X-API-Key: <admin_ключ>" http://localhost:8080/api/keys

# Отзыв ключа (ID из списка)
curl -X DELETE http://localhost:8080/api/keys/5 \
  -H "X-API-Key: <admin_ключ>"
```

---

## 8. Работа с репликами

### 8.1. На реплике: просмотр статуса

```shell
curl http://localhost:8080/api/sync/status
```

**Ответ:**
```json
{
  "success": true,
  "message": "REPLICA статус",
  "data": {
    "role": "replica",
    "master_url": "http://10.10.10.3:8080",
    "sync_interval": "30",
    "last_sync": "2026-05-27T10:30:00Z",
    "sync_enabled": true
  }
}
```

### 8.2. На реплике: последнее обновление

```shell
curl http://localhost:8080/api/sync/last-update
```

### 8.3. На мастере: список зон для синхронизации

**Требуется `X-Sync-Token`**

```shell
curl -H "X-Sync-Token: <токен>" http://localhost:8080/api/sync/zones
```


### 8.4. На мастере: получение A/AAAA записей зоны

```shell script
curl -H "X-Sync-Token: <токен>" \
  http://localhost:8080/api/sync/zone/example.local/records
```

**Ответ:**
```json
{
  "success": true,
  "data": {
    "records": [
      {"name": "www", "type": "A", "value": "10.10.10.100"},
      {"name": "api", "type": "AAAA", "value": "2001:db8::1"}
    ]
  }
}
```


---

## 9. Практические сценарии

### 9.1. Сценарий 1: Добавление нового веб-сервера

```shell
# 1. Создаём зону
curl -X POST http://localhost:8080/api/write/zone \
  -H "Content-Type: application/json" -H "X-API-Key: <ключ>" \
  -d '{"name": "company.local", "email": "admin.company.local", "ns_ip": "10.10.10.1"}'

# 2. Добавляем A-запись для веб-сервера
curl -X POST http://localhost:8080/api/write/zone/company.local/record \
  -H "Content-Type: application/json" -H "X-API-Key: <ключ>" \
  -d '{"name": "www", "type": "A", "value": "10.10.10.100"}'

# 3. Проверяем
nslookup www.company.local <master-ip>
```

### 9.2. Сценарий 2: Настройка почтового сервера

```shell script
# 1. Добавляем MX-запись
curl -X POST http://localhost:8080/api/write/zone/company.local/record \
  -H "Content-Type: application/json" -H "X-API-Key: <ключ>" \
  -d '{"name": "@", "type": "MX", "value": "10 mail.company.local."}'

# 2. Добавляем A-запись для почтовика
curl -X POST http://localhost:8080/api/write/zone/company.local/record \
  -H "Content-Type: application/json" -H "X-API-Key: <ключ>" \
  -d '{"name": "mail", "type": "A", "value": "10.10.10.101"}'

# 3. Добавляем SPF-запись (TXT)
curl -X POST http://localhost:8080/api/write/zone/company.local/record \
  -H "Content-Type: application/json" -H "X-API-Key: <ключ>" \
  -d '{"name": "@", "type": "TXT", "value": "v=spf1 mx ~all"}'
```

### 9.3. Сценарий 3: Настройка Reverse DNS

При добавлении A-записи можно автоматически создать PTR-запись:

```shell
curl -X POST http://localhost:8080/api/write/zone/company.local/record \
  -H "Content-Type: application/json" -H "X-API-Key: <ключ>" \
  -d '{
    "name": "web",
    "type": "A",
    "value": "10.10.10.100",
    "reverse_ptr": "web.company.local."
  }'
```

### 9.4. Сценарий 4: Автоматизация с помощью скрипта

```shell
#!/bin/bash
# add-dns-record.sh

API_URL="http://localhost:8080/api"
API_KEY="your-api-key-here"

add_record() {
    local zone=$1
    local name=$2
    local type=$3
    local value=$4
    
    curl -s -X POST "$API_URL/write/zone/$zone/record" \
        -H "Content-Type: application/json" \
        -H "X-API-Key: $API_KEY" \
        -d "{\"name\": \"$name\", \"type\": \"$type\", \"value\": \"$value\"}"
}

add_record "example.local" "test" "A" "192.168.1.200"
```

### 9.5. Сценарий 5: Проверка статуса с уведомлением

```shell
#!/bin/bash
# check-status.sh

API_URL="http://localhost:8080/api"
API_KEY="your-api-key-here"

response=$(curl -s -H "X-API-Key: $API_KEY" "$API_URL/status")
success=$(echo "$response" | jq -r '.success')
named_status=$(echo "$response" | jq -r '.data.named_status')

if [ "$success" = "true" ] && [ "$named_status" = "active" ]; then
    echo "✅ DNS сервер работает нормально"
else
    echo "❌ Проблема с DNS сервером!"
    echo "$response"
fi
```

---

## 10. Обработка ошибок

### 10.1. Коды HTTP ответов

| Код | Значение |
|-----|----------|
| 200 | Успех |
| 201 | Создано (API-ключ) |
| 400 | Ошибка валидации |
| 401 | Неверный или отсутствующий API-ключ |
| 403 | Недостаточно прав |
| 404 | Ресурс не найден |
| 500 | Внутренняя ошибка сервера |

### 10.2. Примеры ошибок

#### Неверный API-ключ

```json
{
  "success": false,
  "message": "Неверный API-ключ"
}
```

#### Недостаточно прав

```json
{
  "success": false,
  "message": "Недостаточно прав: требуется zone:write"
}
```

#### Ошибка валидации зоны

```json
{
  "success": false,
  "message": "Ошибка валидации JSON",
  "data": "Key: 'ZoneRequest.Name' Error:Field validation for 'Name' failed on the 'required' tag"
}
```

#### Зона не найдена

```json
{
  "success": false,
  "message": "Зона не найдена в конфигурации"
}
```

#### IP заблокирован

```json
{
  "success": false,
  "message": "IP адрес временно заблокирован"
}
```
### 10.3. Отладка

```shell 
# Подробный вывод curl
curl -v -H "X-API-Key: <ключ>" http://localhost:8080/api/read/zones

# Просмотр логов API
sudo journalctl -u bind-api -f

# Проверка прав API-ключа
curl -H "X-API-Key: <ключ>" http://localhost:8080/api/keys

# Проверка заблокированных IP
sudo journalctl -u bind-api | grep "заблокирован"
```

---

## 11. Примеры скриптов

### 11.1. Полный скрипт для добавления зоны и записей

```shell script
#!/bin/bash
# deploy-dns-zone.sh

set -e

API_URL="http://localhost:8080/api"
API_KEY="your-api-key"

ZONE_NAME="myapp.local"
NS_IP="10.10.10.1"
WEB_IP="10.10.10.100"
DB_IP="10.10.10.101"

echo "=== Создание DNS-зоны ==="
curl -X POST "$API_URL/write/zone" \
    -H "Content-Type: application/json" \
    -H "X-API-Key: $API_KEY" \
    -d "{\"name\": \"$ZONE_NAME\", \"ns_ip\": \"$NS_IP\"}"

echo -e "\n=== Добавление A-записей ==="
curl -X POST "$API_URL/write/zone/$ZONE_NAME/record" \
    -H "Content-Type: application/json" -H "X-API-Key: $API_KEY" \
    -d "{\"name\": \"www\", \"type\": \"A\", \"value\": \"$WEB_IP\"}"

curl -X POST "$API_URL/write/zone/$ZONE_NAME/record" \
    -H "Content-Type: application/json" -H "X-API-Key: $API_KEY" \
    -d "{\"name\": \"db\", \"type\": "A", "value": "$DB_IP"}"

echo -e "\n=== Добавление CNAME ==="
curl -X POST "$API_URL/write/zone/$ZONE_NAME/record" \
    -H "Content-Type: application/json" -H "X-API-Key: $API_KEY" \
    -d "{\"name\": \"api\", \"type\": \"CNAME\", \"value\": \"www.$ZONE_NAME.\"}"

echo -e "\n=== Проверка ==="
curl -s -H "X-API-Key: $API_KEY" "$API_URL/read/zone/$ZONE_NAME" | jq '.data.records[] | {name, type, value}'

echo -e "\n DNS-зона $ZONE_NAME успешно развёрнута!"
```

### 11.2. Скрипт для мониторинга реплики

```shell
#!/bin/bash
# check-replica-sync.sh

REPLICA_URL="http://100.69.13.4:8080"
API_KEY="your-api-key"

check_replica() {
    local response=$(curl -s -H "X-API-Key: $API_KEY" "$REPLICA_URL/api/sync/status")
    local last_sync=$(echo "$response" | jq -r '.data.last_sync')
    local sync_enabled=$(echo "$response" | jq -r '.data.sync_enabled')
    
    echo "Реплика: $REPLICA_URL"
    echo "Последняя синхронизация: $last_sync"
    echo "Синхронизация включена: $sync_enabled"
    
    # Проверка что синхронизация была не позже 5 минут
    local last_sync_ts=$(date -d "$last_sync" +%s)
    local now_ts=$(date +%s)
    local diff=$((now_ts - last_sync_ts))
    
    if [ $diff -gt 300 ]; then
        echo "⚠️ ВНИМАНИЕ: Реплика не синхронизировалась более 5 минут!"
        return 1
    fi
    
    echo "Реплика синхронизирована нормально"
    return 0
}

check_replica
```

### 11.3. Скрипт для бэкапа всех зон

```shell
#!/bin/bash
# backup-zones.sh

API_URL="http://localhost:8080/api"
API_KEY="your-api-key"
BACKUP_DIR="/backup/dns/$(date +%Y%m%d_%H%M%S)"

mkdir -p "$BACKUP_DIR"

# Получение списка зон
zones=$(curl -s -H "X-API-Key: $API_KEY" "$API_URL/read/zones" | jq -r '.data.zones[].name')

for zone in $zones; do
    echo "Бэкап зоны: $zone"
    curl -s -H "X-API-Key: $API_KEY" "$API_URL/read/zone/$zone" \
        | jq . > "$BACKUP_DIR/${zone}.json"
done

echo "Бэкап сохранён в $BACKUP_DIR"
```

---

## 📋 Шпаргалка по командам

| Действие | Команда |
|----------|---------|
| Проверка статуса | `GET /api/status` |
| Список зон | `GET /api/read/zones` |
| Информация о зоне | `GET /api/read/zone/{name}` |
| Создание зоны | `POST /api/write/zone` |
| Удаление зоны | `DELETE /api/write/zone/{name}` |
| Добавление записи | `POST /api/write/zone/{name}/record` |
| Удаление записи | `DELETE /api/write/zone/{name}/record/{record}/{type}` |
| Перезагрузка BIND | `POST /api/write/reload` |
| Журнал аудита | `GET /api/read/audit` |
| Создание API-ключа | `POST /api/keys` |
| Список ключей | `GET /api/keys` |
| Отзыв ключа | `DELETE /api/keys/{id}` |
| Health check | `GET /api/health` |
| Prometheus-метрики | `GET /metrics` |

---

**© 2026 BIND API | Версия 0.4.10**

