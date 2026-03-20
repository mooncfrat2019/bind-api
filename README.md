# 📘 BIND Manager API

**Репозиторий:** [github.com/mooncfrat2019/bind-api](https://github.com/mooncfrat2019/bind-api)  
**Версия:** 0.2.0  
**Последнее обновление:** Март 2026

---

## 📋 Содержание

1. [Обзор](#1-обзор)
2. [Архитектура](#2-архитектура)
3. [Быстрый старт](#3-быстрый-старт)
4. [Конфигурация](#4-конфигурация)
5. [API Reference](#5-api-reference)
6. [Версионирование и откат](#6-версионирование-и-откат)
7. [Master-Replica синхронизация](#7-master-replica-синхронизация)
8. [Безопасность](#8-безопасность)
9. [Мониторинг и отладка](#9-мониторинг-и-отладка)
10. [Troubleshooting](#10-troubleshooting)

---

## 1. Обзор

**BIND Manager API** — это REST API сервис для управления DNS-сервером BIND (named) с поддержкой Master-Replica архитектуры, версионирования конфигурации и автоматической синхронизации.

### Возможности

| Функция | Описание |
|---------|----------|
| Управление зонами | Создание, удаление, просмотр DNS-зон |
| Управление записями | Добавление/удаление A, AAAA, CNAME, MX, TXT, NS записей |
| Reverse DNS | Автоматическое создание PTR записей |
| Очередь заданий | Последовательная обработка для защиты от race conditions |
| Аудит операций | Полное логирование всех изменений в PostgreSQL |
| Валидация | Проверка синтаксиса перед применением (`named-checkconf`, `named-checkzone`) |
| Serial management | Автоматическое увеличение Serial при изменениях |
| Версионирование | Сохранение всех версий конфигов с возможностью отката |
| Master-Replica | Автоматическая синхронизация конфигурации между серверами |
| Трансформация конфигов | Автоматическая конвертация master→slave при синхронизации |

### Технологии

| Компонент | Технология |
|-----------|------------|
| Язык | Go 1.21+ |
| Web Framework | Gin |
| ORM | GORM |
| База данных | PostgreSQL 13+ |
| DNS Server | BIND 9.11+ |
| ОС | РедОС 7.3 / CentOS 7+ |

---

## 2. Архитектура

### 2.1. Общая схема

```
┌─────────────────────────────────────────────────────────────────┐
│                        КЛИЕНТЫ (curl, UI)                       │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      GIN HTTP SERVER                            │
│                    (порт 8080, REST API)                        │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                         HANDLERS                                │
│  handleCreateZone, handleAddRecord, handleDeleteZone, etc.      │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      ОЧЕРЕДЬ ЗАДАНИЙ                            │
│              chan *Job (in-memory, буфер 100)                   │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      WORKER (1 горутина)                        │
│              jobWorker() - обрабатывает последовательно         │
└─────────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
    ┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
    │  FILE LOCKS     │ │  BIND OPERATIONS│ │   PostgreSQL    │
    │  (sync.Mutex)   │ │  (rndc, named)  │ │   (Audit +      │
    └─────────────────┘ └─────────────────┘ │    Versions)    │
                                            └─────────────────┘
```

### 2.2. Master-Replica синхронизация

```
┌─────────────────────────────────┐      ┌─────────────────────────────────┐
│         MASTER SERVER           │      │        REPLICA SERVER           │
│  APP_ROLE=master                │      │  APP_ROLE=replica               │
│                                 │      │                                 │
│  ┌─────────────────────────┐    │      │                                 │
│  │   PostgreSQL            │    │      │                                 │
│  │   - audit_logs          │    │      │                                 │
│  │   - sync_states         │    │      │                                 │
│  │   (версии конфигов)     │    │      │                                 │
│  └─────────────────────────┘    │      │                                 │
│              │                  │      │                                 │
│              ▼                  │      │                                 │
│  ┌─────────────────────────┐    │      │  ┌─────────────────────────┐    │
│  │   SyncHandler API       │    │      │  │   ReplicaSync Client    │    │
│  │   - /api/sync/*         │────┼─────►│  │   - Poll каждые 30 сек  │    │
│  │   - версии конфигов     │    │      │  │   - трансформация       │    │
│  └─────────────────────────┘    │      │  │   - сохранение локально │    │
│                                 │      │  └─────────────────────────┘    │
│              │                  │      │              │                  │
│              ▼                  │      │              ▼                  │
│  ┌─────────────────────────┐    │      │  ┌─────────────────────────┐    │
│  │   BIND (master)         │    │      │  │   BIND (slave)          │    │
│  │   - allow-transfer      │    │      │  │   - masters { master; } │    │
│  │   - also-notify         │────┼─────►│  │                         │    │
│  └─────────────────────────┘    │      │  └─────────────────────────┘    │
│                                 │      │                                 │
│  Файлы зон:                     │      │  Файлы зон:                     │
│  /var/named/*.zone              │      │  /var/named/slaves/*.zone       │
│  (передаются через BIND AXFR)   │      │  (получаются через BIND AXFR)   │
└─────────────────────────────────┘      └─────────────────────────────────┘
```

### 2.3. Поток выполнения запроса

```
1. HTTP Request → Handler
2. Handler → Создаёт Job → Отправляет в channel
3. Handler → Ждёт ответа в ResponseCh (таймаут 30 сек)
4. Worker → Читает из channel → Выполняет операцию
5. Worker → Пишет аудит в PostgreSQL
6. Worker → Сохраняет версию конфига (если изменился)
7. Worker → Возвращает результат в ResponseCh
8. Handler → Возвращает ответ клиенту
9. (MASTER) → Обновляет sync_states для реплик
10. (REPLICA) → Периодически опрашивает /api/sync/state
11. (REPLICA) → Скачивает изменённые конфиги → трансформирует → сохраняет
12. (REPLICA) → Перезагружает BIND при изменениях
```

---

## 3. Быстрый старт

### 3.1. Требования

- Go 1.21+
- PostgreSQL 13+
- BIND 9.11+
- Root-доступ к серверу

### 3.2. Установка зависимостей

```bash
# Клонирование репозитория
git clone https://github.com/mooncfrat2019/bind-api.git
cd bind-api

# Установка зависимостей
go mod tidy
```

### 3.3. Настройка PostgreSQL

```bash
# Подключиться к PostgreSQL
sudo -u postgres psql

# Создать пользователя и базу
CREATE USER dns WITH PASSWORD 'your_secure_password';
CREATE DATABASE dns OWNER dns;

# Создать схему и выдать права
\c dns
CREATE SCHEMA IF NOT EXISTS bind_api;
GRANT ALL PRIVILEGES ON SCHEMA bind_api TO dns;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA bind_api TO dns;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA bind_api TO dns;
ALTER DEFAULT PRIVILEGES IN SCHEMA bind_api GRANT ALL ON TABLES TO dns;
ALTER DEFAULT PRIVILEGES IN SCHEMA bind_api GRANT ALL ON SEQUENCES TO dns;

# Выйти
\q
```

### 3.4. Настройка BIND на мастере

```bash
# Убедиться что named.conf включает доп. конфиг
echo 'include "/etc/named.zones.conf";' | sudo tee -a /etc/named.conf

# Создать файл для зон
sudo touch /etc/named.zones.conf
sudo chown root:named /etc/named.zones.conf
sudo chmod 640 /etc/named.zones.conf

# Настроить allow-transfer для реплик
sudo nano /etc/named.conf
# Добавить в options:
# allow-transfer { 10.69.13.4; localhost; };
# also-notify { 10.69.13.4; };

# Проверить rndc
sudo rndc-confgen -a -c /etc/rndc.key
sudo chown named:named /etc/rndc.key
sudo chmod 640 /etc/rndc.key

# Перезапустить BIND
sudo systemctl restart named
```

### 3.5. Файл .env для MASTER

```ini
# Роль сервера
APP_ROLE=master

# BIND настройки
BIND_ZONE_DIR=/var/named/
BIND_NAMED_CONF=/etc/named.conf
BIND_ZONE_CONF=/etc/named.zones.conf

# PostgreSQL настройки
BIND_API_DB_HOST=localhost
BIND_API_DB_PORT=5432
BIND_API_DB_USER=dns
BIND_API_DB_PASSWORD=your_secure_password
BIND_API_DB_NAME=dns
BIND_API_DB_SSLMODE=disable
BIND_API_DB_SCHEMA=bind_api

# API настройки
API_PORT=:8080
GIN_MODE=release

# Синхронизация
SYNC_API_TOKEN=your_secure_sync_token_12345
SYNC_ENABLED=true
```

### 3.6. Файл .env для REPLICA

```ini
# Роль сервера
APP_ROLE=replica

# BIND настройки
BIND_ZONE_DIR=/var/named/
BIND_NAMED_CONF=/etc/named.conf
BIND_ZONE_CONF=/etc/named.zones.conf

# API настройки
API_PORT=:8080
GIN_MODE=release

# Синхронизация
MASTER_URL=http://10.10.10.3:8080
MASTER_API_TOKEN=your_secure_sync_token_12345
SYNC_INTERVAL=30
SYNC_ENABLED=true

# Трансформации конфигурации
REPLICA_MASTER_IP=10.10.10.3
REPLICA_ZONE_TYPE=slave
REPLICA_ZONE_SUBDIR=slaves
REPLICA_REMOVE_ALLOW_TRANSFER=true
REPLICA_DISABLE_IPV6=true
```

### 3.7. Сборка и запуск

```bash
# Сборка
CGO_ENABLED=1 go build -o bind-api main.go

# Запуск MASTER
sudo ./bind-api

# Запуск REPLICA (на другом сервере)
sudo ./bind-api
```

### 3.8. Проверка работы

```bash
# Проверить статус
curl http://localhost:8080/api/status | jq .

# Создать зону на мастере
curl -X POST http://master:8080/api/zone \
  -H "Content-Type: application/json" \
  -d '{"name": "test.local", "email": "admin.test.local"}' | jq .

# Добавить запись
curl -X POST http://master:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{"name": "www", "type": "A", "value": "192.168.1.100"}' | jq .

# Подождать синхронизацию (30 сек)
sleep 35

# Проверить на реплике
curl http://replica:8080/api/sync/status | jq .
sudo cat /etc/named.zones.conf | grep -A 5 "test.local"
sudo ls -la /var/named/slaves/test.local.zone
```

---

## 4. Конфигурация

### 4.1. Переменные окружения

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `APP_ROLE` | `master` | Роль сервера: `master` или `replica` |
| `BIND_ZONE_DIR` | `/var/named/` | Директория для файлов зон |
| `BIND_NAMED_CONF` | `/etc/named.conf` | Основной конфиг BIND |
| `BIND_ZONE_CONF` | `/etc/named.zones.conf` | Доп. файл для зон |
| `BIND_API_DB_HOST` | `localhost` | Хост PostgreSQL |
| `BIND_API_DB_PORT` | `5432` | Порт PostgreSQL |
| `BIND_API_DB_USER` | `bindapi` | Пользователь БД |
| `BIND_API_DB_PASSWORD` | — | Пароль БД |
| `BIND_API_DB_NAME` | `bind_api` | Имя базы данных |
| `BIND_API_DB_SSLMODE` | `disable` | SSL режим |
| `BIND_API_DB_SCHEMA` | `public` | Схема для таблиц |
| `API_PORT` | `:8080` | Порт API |
| `GIN_MODE` | `release` | Режим Gin (debug/release) |
| `SYNC_API_TOKEN` | — | Токен для синхронизации (MASTER) |
| `MASTER_URL` | — | URL мастера (REPLICA) |
| `MASTER_API_TOKEN` | — | Токен для подключения к мастеру (REPLICA) |
| `SYNC_INTERVAL` | `30` | Интервал опроса мастера в секундах |
| `REPLICA_MASTER_IP` | `127.0.0.1` | IP мастера для директивы `masters {}` |
| `REPLICA_ZONE_TYPE` | `slave` | Тип зон на реплике |
| `REPLICA_ZONE_SUBDIR` | `slaves` | Подкаталог для файлов зон на реплике |
| `REPLICA_REMOVE_ALLOW_TRANSFER` | `false` | Удалять `allow-transfer` на реплике |
| `REPLICA_DISABLE_IPV6` | `false` | Отключать IPv6 на реплике |

### 4.2. Приоритет конфигурации

```
1. Переменные окружения системы
2. Файл .env в директории запуска
3. Значения по умолчанию
```

---

## 5. API Reference

### 5.1. Общие сведения

- **Base URL:** `http://localhost:8080/api`
- **Content-Type:** `application/json`
- **Формат ответа:**
```json
{
  "success": true,
  "message": "Описание результата",
  "data": {...}
}
```

### 5.2. Эндпоинты

#### Общие эндпоинты (доступны на MASTER и REPLICA)

| Метод | Эндпоинт | Описание | Доступно на |
|-------|----------|----------|-------------|
| `GET` | `/status` | Статус сервиса | MASTER, REPLICA |
| `GET` | `/sync/status` | Статус синхронизации | REPLICA |
| `GET` | `/sync/last-update` | Последнее обновление | REPLICA |

#### Эндпоинты управления зонами (только MASTER)

| Метод | Эндпоинт | Описание |
|-------|----------|----------|
| `GET` | `/zones` | Список всех зон |
| `POST` | `/zone` | Создание зоны |
| `GET` | `/zone/:name` | Информация о зоне |
| `DELETE` | `/zone/:name` | Удаление зоны |
| `POST` | `/zone/:name/record` | Добавление записи |
| `DELETE` | `/zone/:name/record/:record/:type` | Удаление записи |
| `POST` | `/reload` | Перезагрузка BIND |
| `GET` | `/config` | Конфигурация API |
| `GET` | `/audit` | Журнал аудита |
| `GET` | `/audit/stats` | Статистика аудита |

#### Эндпоинты синхронизации (только MASTER, требуют `X-Sync-Token`)

| Метод | Эндпоинт | Описание |
|-------|----------|----------|
| `GET` | `/sync/state` | Состояние всех файлов |
| `GET` | `/sync/file` | Получить файл (query params) |
| `GET` | `/sync/zones` | Список зон для синхронизации |
| `GET` | `/sync/zone/:zoneName` | Получить зону |
| `GET` | `/sync/versions/:fileType` | Список версий файла |
| `GET` | `/sync/version/:id` | Конкретная версия |
| `POST` | `/sync/version/:id/rollback` | Откат к версии |
| `DELETE` | `/sync/version/:id` | Удаление версии |

### 5.3. Детальное описание

#### Создание зоны

**Метод:** `POST`  
**URL:** `/api/zone`

**Параметры запроса:**

| Поле | Тип | Обязательное | Описание |
|------|-----|--------------|----------|
| `name` | string | ✅ | Имя зоны (test.local) |
| `email` | string | ❌ | Email админа (по умолчанию admin.<name>) |
| `ns_ip` | string | ❌ | IP для ns1 (авто-определение) |
| `config_file` | string | ❌ | Путь к конфигу (авто-выбор) |

**Пример:**
```bash
curl -X POST http://localhost:8080/api/zone \
  -H "Content-Type: application/json" \
  -d '{"name": "test.local", "email": "admin.test.local", "ns_ip": "10.69.13.3"}'
```

**Ответ:**
```json
{
  "success": true,
  "message": "Зона test.local (forward) создана в /etc/named.conf",
  "data": {
    "zone_file": "/var/named/test.local.zone",
    "config_file": "/etc/named.conf",
    "ns1_ip": "10.69.13.3"
  }
}
```

#### Добавление записи

**Метод:** `POST`  
**URL:** `/api/zone/:name/record`

**Параметры запроса:**

| Поле | Тип | Обязательное | Описание |
|------|-----|--------------|----------|
| `name` | string | ✅ | Имя записи (www, @, mail) |
| `type` | string | ✅ | Тип (A/AAAA/CNAME/MX/TXT/NS) |
| `value` | string | ✅ | Значение (IP, домен, текст) |
| `ttl` | int | ❌ | TTL в секундах (3600) |
| `reverse_ptr` | string | ❌ | Имя для PTR (только для A/AAAA) |

**Примеры:**

```bash
# A запись
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{"name": "www", "type": "A", "value": "192.168.1.100"}'

# A запись с PTR
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{"name": "mail", "type": "A", "value": "192.168.1.101", "reverse_ptr": "mail.test.local"}'

# CNAME
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{"name": "blog", "type": "CNAME", "value": "www.test.local"}'

# MX
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{"name": "@", "type": "MX", "value": "10 mail.test.local"}'

# TXT (SPF)
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{"name": "@", "type": "TXT", "value": "v=spf1 include:_spf.google.com ~all"}'
```

#### Получение списка версий файла

**Метод:** `GET`  
**URL:** `/api/sync/versions/:fileType?fileName=...`

**Заголовок:** `X-Sync-Token: <token>`

**Параметры:**

| Параметр | Тип | Описание |
|----------|-----|----------|
| `fileType` | path | Тип файла: `named_conf`, `zone_conf`, `zone_file` |
| `fileName` | query | Имя файла (URL-encoded) |
| `limit` | query | Максимум записей (по умолчанию 50) |

**Пример:**
```bash
curl -H "X-Sync-Token: your_token" \
  "http://master:8080/api/sync/versions/zone_file?fileName=%2Fvar%2Fnamed%2Ftest.local.zone" | jq .
```

**Ответ:**
```json
{
  "success": true,
  "message": "Версии получены",
  "data": {
    "file_type": "zone_file",
    "file_name": "/var/named/test.local.zone",
    "versions": [
      {
        "id": 15,
        "version": 3,
        "checksum": "abc123...",
        "last_modified": "2024-01-01T12:05:00Z"
      },
      {
        "id": 12,
        "version": 2,
        "checksum": "def456...",
        "last_modified": "2024-01-01T12:00:00Z"
      }
    ]
  }
}
```

#### Откат к версии

**Метод:** `POST`  
**URL:** `/api/sync/version/:id/rollback`

**Заголовок:** `X-Sync-Token: <token>`

**Параметры:**

| Параметр | Тип | Описание |
|----------|-----|----------|
| `id` | path | ID версии из списка версий |
| `force` | query | Пропустить проверку синтаксиса (true/false) |

**Пример:**
```bash
# Обычный откат с проверкой синтаксиса
curl -X POST -H "X-Sync-Token: your_token" \
  http://master:8080/api/sync/version/12/rollback | jq .

# Экстренный откат без проверки (если BIND не запускается)
curl -X POST -H "X-Sync-Token: your_token" \
  "http://master:8080/api/sync/version/12/rollback?force=true" | jq .
```

**Ответ:**
```json
{
  "success": true,
  "message": "Откат к версии 2 выполнен",
  "data": {
    "version": 2,
    "file_name": "/var/named/test.local.zone",
    "file_type": "zone_file",
    "checksum": "def456..."
  }
}
```

---

## 6. Версионирование и откат

### 6.1. Как работает версионирование

1.  При любом изменении конфига (создание зоны, добавление записи) контент файла сохраняется в таблицу `sync_states` с новым номером версии.
2.  Каждая версия хранится как отдельная запись с уникальным `id`.
3.  Старые версии не перезаписываются — история сохраняется полностью.
4.  Через API можно получить список версий, контент конкретной версии или выполнить откат.

### 6.2. Структура таблицы `sync_states`

```sql
CREATE TABLE sync_states (
    id BIGSERIAL PRIMARY KEY,
    file_type VARCHAR(50) NOT NULL,
    file_name VARCHAR(500) NOT NULL,
    zone_name VARCHAR(255),
    checksum VARCHAR(64) NOT NULL,
    version INT NOT NULL,
    content TEXT,
    last_modified TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ
);

-- Индексы для быстрого поиска
CREATE INDEX idx_sync_states_file_type ON sync_states(file_type);
CREATE INDEX idx_sync_states_file_name ON sync_states(file_name);
CREATE INDEX idx_sync_states_version ON sync_states(version);
CREATE UNIQUE INDEX idx_sync_states_unique_version 
    ON sync_states(file_type, file_name, version);
```

### 6.3. Сценарии использования

#### Просмотр истории изменений зоны

```bash
# Получить все версии файла зоны
curl -H "X-Sync-Token: your_token" \
  "http://master:8080/api/sync/versions/zone_file?fileName=%2Fvar%2Fnamed%2Ftest.local.zone" | jq .

# Получить контент конкретной версии
curl -H "X-Sync-Token: your_token" \
  http://master:8080/api/sync/version/12 | jq .data.content
```

#### Откат после ошибочного изменения

```bash
# 1. Добавить ошибочную запись
curl -X POST http://master:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{"name": "bad", "type": "A", "value": "0.0.0.0"}'

# 2. Получить список версий и найти ID предыдущей
curl -H "X-Sync-Token: your_token" \
  "http://master:8080/api/sync/versions/zone_file?fileName=%2Fvar%2Fnamed%2Ftest.local.zone" | jq '.data.versions[1].id'

# 3. Откатиться к предыдущей версии
curl -X POST -H "X-Sync-Token: your_token" \
  http://master:8080/api/sync/version/<PREVIOUS_ID>/rollback

# 4. Проверить что запись bad исчезла
curl http://master:8080/api/zone/test.local | jq '.data.records[] | select(.name=="bad")'
```

#### Очистка старых версий

```bash
# Удалить конкретную старую версию
curl -X DELETE -H "X-Sync-Token: your_token" \
  http://master:8080/api/sync/version/5

# Автоматическая очистка (настраивается в коде)
# По умолчанию: хранить версии за последние 30 дней, максимум 50 версий на файл
```

---

## 7. Master-Replica синхронизация

### 7.1. Принцип работы

| Компонент | Мастер | Реплика |
|-----------|--------|---------|
| Конфигурация | Создаёт/изменяет | Получает через API |
| Файлы зон | Создаёт/изменяет | Получает через BIND AXFR |
| База данных | Хранит версии и аудит | Опционально (только для локального аудита) |
| Трансформация | Отдаёт "сырой" конфиг | Применяет трансформации перед сохранением |

### 7.2. Трансформация конфигурации на реплике

При получении конфига с мастера реплика автоматически применяет следующие изменения:

| Директива | На мастере | На реплике |
|-----------|------------|------------|
| `type` | `master` | `slave` |
| `masters` | отсутствует | `masters { <REPLICA_MASTER_IP>; };` |
| `file` | `"zone.zone"` | `"slaves/zone.zone"` |
| `allow-update` | `{ none; }` | удаляется |
| `allow-transfer` | `{ ... }` | удаляется (если `REPLICA_REMOVE_ALLOW_TRANSFER=true`) |
| `listen-on-v6` | `{ any; }` | `{ none; }` (если `REPLICA_DISABLE_IPV6=true`) |

### 7.3. Настройка BIND для zone transfer

**На мастере (`/etc/named.conf`):**
```bind
options {
    allow-transfer { 10.69.13.4; localhost; };
    also-notify { 10.69.13.4; };
};

zone "test.local" IN {
    type master;
    file "test.local.zone";
    allow-transfer { 10.69.13.4; };
};
```

**На реплике (после трансформации):**
```bind
zone "test.local" IN {
    type slave;
    masters { 10.69.13.3; };
    file "slaves/test.local.zone";
};
```

### 7.4. Проверка синхронизации

```bash
# На мастере: проверить состояние файлов
curl -H "X-Sync-Token: your_token" \
  http://master:8080/api/sync/state | jq '.data.files[] | {file_type, file_name, version}'

# На реплике: проверить статус синхронизации
curl http://replica:8080/api/sync/status | jq .

# Проверить логи синхронизации на реплике
sudo journalctl -u bind-api -f | grep "Синхронизация"

# Проверить что зоны получены через BIND AXFR
sudo ls -la /var/named/slaves/
sudo named-checkzone test.local /var/named/slaves/test.local.zone
```

---

## 8. Безопасность

### 8.1. Требования

| Требование | Рекомендация |
|------------|--------------|
| Запуск от | root (для доступа к /etc, /var/named) |
| Порт API | Закрыть фаерволом для внешних сетей |
| PostgreSQL | Ограничить доступ по IP |
| rndc ключ | 640 named:named |
| Токен синхронизации | Сложный, уникальный, хранить в секретах |

### 8.2. Настройка фаервола

```bash
# Разрешить только локальный доступ к API
sudo firewall-cmd --add-port=8080/tcp --zone=internal --permanent
sudo firewall-cmd --reload

# Или ограничить по подсети
sudo firewall-cmd --add-rich-rule='rule family="ipv4" source address="10.69.13.0/24" port port="8080" protocol="tcp" accept' --permanent
```

### 8.3. Рекомендации для продакшена

1.  **HTTPS:** Использовать nginx как reverse proxy с SSL
2.  **Авторизация:** Добавить Basic Auth или JWT для управления
3.  **Rate Limiting:** Ограничить количество запросов к API
4.  **Аудит:** Включить логирование всех запросов
5.  **Бэкапы:** Регулярный бэкап PostgreSQL и зон
6.  **Мониторинг:** Настроить алерты на ошибки синхронизации

---

## 9. Мониторинг и отладка

### 9.1. Логи приложения

```bash
# Журнал systemd
sudo journalctl -u bind-api -f

# Логи в реальном времени
sudo ./bind-api 2>&1 | tee /var/log/bind-api.log
```

### 9.2. Логи BIND

```bash
# Основные логи
sudo tail -f /var/log/messages | grep named

# Проверка синтаксиса
sudo named-checkconf
sudo named-checkzone test.local /var/named/test.local.zone

# Статус rndc
sudo rndc status
```

### 9.3. Проверка очереди и версионирования

```bash
# Размер очереди заданий
curl http://localhost:8080/api/status | jq '.data.queue_size'

# Статистика операций
curl http://localhost:8080/api/audit/stats | jq .

# Последние ошибки
curl "http://localhost:8080/api/audit?status=FAILED&limit=10" | jq '.data.logs[]'

# Версии конкретного файла
curl -H "X-Sync-Token: your_token" \
  "http://master:8080/api/sync/versions/zone_conf?fileName=%2Fetc%2Fnamed.zones.conf" | jq .
```

### 9.4. Проверка БД

```bash
# Подключиться
PGPASSWORD=password psql -h localhost -U dns -d dns

# Последние операции
SELECT * FROM bind_api.audit_logs ORDER BY created_at DESC LIMIT 10;

# Версии файла
SELECT version, checksum, last_modified 
FROM bind_api.sync_states 
WHERE file_name = '/var/named/test.local.zone' 
ORDER BY version DESC;

# Статистика по зонам
SELECT zone_name, COUNT(*) as versions 
FROM bind_api.sync_states 
GROUP BY zone_name 
ORDER BY versions DESC;
```

---

## 10. Troubleshooting

### 10.1. Частые проблемы

| Проблема | Причина | Решение |
|----------|---------|---------|
| `permission denied` | Неправильные права на файлы | `chown named:named`, `chmod 644` |
| `rndc reload failed` | Проблемы с ключами rndc | `rndc-confgen -a`, проверить права |
| `duplicate key value` | Unique index на file_name | Убрать `uniqueIndex` из модели, создать композитный индекс |
| `syntax error near ';'` | Ошибка трансформации конфига | Проверить регулярки, добавить баланс скобок |
| `404 page not found` | Путь с / в параметрах | Использовать query params или URL-encoding |
| `slice bounds out of range` | Пустой checksum | Проверять длину строки перед срезом |
| `no schema has been selected` | Схема не создана в БД | `CREATE SCHEMA bind_api; GRANT ...` |
| `zone transfer failed` | Не настроен allow-transfer | Добавить `allow-transfer { replica_ip; };` на мастере |

### 10.2. Диагностика

```bash
# 1. Проверить права на конфиги
ls -la /etc/named.conf /etc/named.zones.conf

# 2. Проверить права на зоны
ls -la /var/named/*.zone /var/named/slaves/*.zone

# 3. Проверить rndc
sudo rndc status

# 4. Проверить БД
PGPASSWORD=password psql -h localhost -U dns -d dns -c "SELECT version();"

# 5. Проверить очередь
curl http://localhost:8080/api/status | jq .

# 6. Проверить синхронизацию
curl -H "X-Sync-Token: your_token" http://master:8080/api/sync/state | jq .

# 7. Проверить логи
sudo journalctl -u bind-api -n 50
```

### 10.3. Восстановление после сбоя

```bash
# 1. Остановить сервис
sudo systemctl stop bind-api

# 2. Проверить синтаксис конфига
sudo named-checkconf

# 3. Проверить зоны
sudo named-checkzone test.local /var/named/test.local.zone

# 4. Исправить права
sudo chown -R named:named /var/named/
sudo chown root:named /etc/named*.conf
sudo chmod 640 /etc/named*.conf
sudo chmod 644 /var/named/*.zone

# 5. Откатиться к рабочей версии (если нужно)
curl -X POST -H "X-Sync-Token: your_token" \
  http://master:8080/api/sync/version/<WORKING_VERSION_ID>/rollback

# 6. Перезапустить BIND
sudo systemctl restart named

# 7. Запустить API
sudo systemctl start bind-api
```

---

## 📞 Поддержка

При возникновении проблем:

1.  Проверьте логи (`journalctl -u bind-api`)
2.  Проверьте аудит (`/api/audit?status=FAILED`)
3.  Проверьте синтаксис BIND (`named-checkconf`)
4.  Проверьте права на файлы
5.  Убедитесь что токен синхронизации совпадает на мастере и реплике

---
