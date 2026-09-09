# BIND-API Specification

## 1. Общая информация

**Название проекта:** BIND-API  
**Версия API:** 1.0.0  
**Язык реализации:** Go 1.26  
**Лицензия:** MIT

## 2. Назначение

REST API сервис для управления DNS-сервером BIND. Предоставляет возможности создания, удаления и модификации DNS-зон и записей, а также синхронизацию между мастер- и реплика-серверами.

## 3. Архитектура

### 3.1. Режимы работы

| Режим | Описание |
|-------|----------|
| **MASTER** | Основной сервер управления зонами, хранит состояние в PostgreSQL |
| **REPLICA** | Реплика, синхронизируется с мастером через API |

### 3.2. Компоненты

```
┌─────────────────┐     ┌─────────────────┐
│   BIND-API      │────▶│  PostgreSQL     │
│   (Master)      │     │  (audit, keys,  │
└─────────────────┘     │   sync_state)   │
        │               └─────────────────┘
        │ API Sync
        ▼
┌─────────────────┐
│   BIND-API      │
│   (Replica)     │
└─────────────────┘
```


## 4. API Endpoints

### 4.1. Публичные endpoints

| Метод | Endpoint | Описание |
|-------|----------|----------|
| GET | `/health` | Проверка здоровья сервиса |
| GET | `/metrics` | Prometheus-метрики |
| GET | `/api/status` | Статус сервиса |

### 4.2. Endpoints для MASTER (требуют авторизации)

#### 4.2.1. Управление зонами (`zone:read` / `zone:write`)

| Метод | Endpoint | Права | Описание |
|-------|----------|-------|----------|
| GET | `/api/read/zones` | zone:read | Список всех зон |
| GET | `/api/read/zone/:name` | zone:read | Информация о зоне |
| POST | `/api/write/zone` | zone:write | Создание зоны |
| DELETE | `/api/write/zone/:name` | zone:write | Удаление зоны |

#### 4.2.2. Управление записями (`zone:write`)

| Метод | Endpoint | Описание |
|-------|----------|----------|
| POST | `/api/write/zone/:name/record` | Добавить запись |
| DELETE | `/api/write/zone/:name/record/:record/:type` | Удалить запись |

#### 4.2.3. Конфигурация и аудит (`zone:read`)

| Метод | Endpoint | Описание |
|-------|----------|----------|
| GET | `/api/read/config` | Текущая конфигурация |
| GET | `/api/read/audit` | Журнал аудита |
| GET | `/api/read/audit/stats` | Статистика аудита |

#### 4.2.4. Управление API-ключами (`admin`)

| Метод | Endpoint | Описание |
|-------|----------|----------|
| POST | `/api/keys` | Создать API-ключ |
| GET | `/api/keys` | Список ключей |
| DELETE | `/api/keys/:id` | Отозвать ключ |

#### 4.2.5. Синхронизация (X-Sync-Token)

| Метод | Endpoint | Описание |
|-------|----------|----------|
| GET | `/api/sync/state` | Состояние синхронизации |
| GET | `/api/sync/zones` | Список зон |
| GET | `/api/sync/zone/:zoneName` | Данные зоны |
| GET | `/api/sync/zone/:zoneName/records` | A/AAAA записи зоны |
| GET | `/api/sync/file` | Файл конфигурации |
| GET | `/api/sync/versions/:fileType` | Версии файла |
| POST | `/api/sync/version/:id/rollback` | Откат версии |
| DELETE | `/api/sync/version/:id` | Удалить версию |

### 4.3. Endpoints для REPLICA

| Метод | Endpoint | Описание |
|-------|----------|----------|
| GET | `/api/sync/status` | Статус реплики |
| GET | `/api/sync/last-update` | Последняя синхронизация |

## 5. Типы DNS записей

Поддерживаемые типы записей:
- **A** - IPv4 адрес
- **AAAA** - IPv6 адрес
- **CNAME** - Canonical name
- **MX** - Mail exchange
- **TXT** - Text record
- **NS** - Name server
- **PTR** - Pointer (автоматически для A/AAAA)

## 6. Конфигурация

### 6.1. Переменные окружения

#### PostgreSQL
```textmate
BIND_API_DB_HOST=localhost
BIND_API_DB_PORT=5432
BIND_API_DB_USER=bindapi
BIND_API_DB_PASSWORD=your_secure_password
BIND_API_DB_NAME=bind_api
BIND_API_DB_SSLMODE=disable
BIND_API_DB_URL=postgres://...  # альтернатива
BIND_API_BOOTSTRAP_KEY=  # временный ключ (32-120 символов)
```


#### BIND
```textmate
BIND_ZONE_DIR=/var/named/
BIND_NAMED_CONF=/etc/named.conf
```


#### API
```textmate
API_PORT=:8080
APP_ROLE=master  # master | replica
```


#### Очередь заданий
```textmate
MAX_QUEUE_SIZE=1000
WORKER_TIMEOUT=180
BATCH_SIZE=50
BATCH_INTERVAL=5
QUEUE_THRESHOLD_LOW=0.1
QUEUE_THRESHOLD_HIGH=0.3
RELOAD_INTERVAL=10
```


#### Синхронизация
```textmate
SYNC_API_TOKEN=your_secure_sync_token_12345
SYNC_API_SUBNET=10.10.10.0/24
MASTER_URL=https://master.example.com  # для REPLICA
MASTER_API_TOKEN=token  # для REPLICA
SYNC_INTERVAL=30
ALLOW_INSECURE_SYNC=false
```


#### Логирование
```textmate
LOG_LEVEL=INFO  # DEBUG | INFO | WARN | ERROR
```


## 7. Безопасность

### 7.1. Авторизация

| Метод | Заголовок | Описание |
|-------|-----------|----------|
| API Key | `X-API-Key` | Хешированный ключ с префиксом |
| Sync | `X-Sync-Token` | Токен синхронизации |

### 7.2. Права доступа

| Право | Описание |
|-------|----------|
| `zone:read` | Чтение зон и конфигурации |
| `zone:write` | Создание/изменение зон и записей |
| `admin` | Управление API-ключами |
| `*` | Полный доступ (bootstrap) |

### 7.3. Защита

- Блокировка IP после 5 неудачных попыток (15 минут)
- Валидация входных данных (XSS, инъекции)
- Проверка путей к файлам
- Timing-safe сравнение токенов
- HTTPS рекомендуется для синхронизации

## 8. Зависимости

### 8.1. Go модули
```textmate
github.com/gin-gonic/gin      // Web framework
github.com/joho/godotenv      // .env loader
gorm.io/gorm                  // ORM
gorm.io/driver/postgres       // PostgreSQL driver
```


### 8.2. Системные требования
- BIND9 (named)
- rndc утилита
- named-checkconf
- named-checkzone
- PostgreSQL 12+

## 9. Структура проекта

```
bind-api/
├── main.go                    # Точка входа
├── internal/
│   ├── handlers.go           # HTTP обработчики
│   ├── methods.go            # Бизнес-логика
│   ├── utils.go              # Утилиты
│   ├── buffer.go             # Асинхронный буфер
│   ├── consts.go             # Константы
│   ├── types.go              # Типы данных
│   ├── vars.go               # Глобальные переменные
│   ├── logger.go             # Логирование
│   ├── metrics.go            # Метрики
│   ├── migrations.go         # Миграции БД
│   └── *_test.go             # Тесты
├── test/load/                # Нагрузочное тестирование
├── docker-compose*.yml       # Docker конфигурация
├── .env.example              # Шаблон конфигурации
└── Makefile                  # Сборка
```


## 10. Очередь заданий

### 10.1. Типы заданий
- `CREATE_ZONE` - Создание зоны
- `DELETE_ZONE` - Удаление зоны
- `ADD_RECORD` - Добавление записи
- `DELETE_RECORD` - Удаление записи
- `RELOAD` - Перезагрузка BIND

### 10.2. Режимы работы очереди

| Режим | Порог | Описание |
|-------|-------|----------|
| **NORMAL** | < 10% | Немедленная обработка |
| **BATCH** | > 30% | Пакетная обработка (50 записей / 5 сек) |

## 11. Асинхронный буфер записей

- WAL-логирование для восстановления
- Пакетная запись в файлы зон
- Автоматическое увеличение serial
- Параллельная запись по зонам

## 12. Синхронизация Master-Replica

### 12.1. Мастер
- Хранит версии файлов в БД
- Предоставляет API для синхронизации
- Мониторит изменения named.conf

### 12.2. Реплика
- Периодическая синхронизация (30 сек по умолчанию)
- Трансформация конфигурации (master → slave)
- Проверка резолвинга A записей
- Автоматический retransfer при проблемах

## 13. Метрики

- Количество операций по типам
- Время выполнения операций
- Статус реплик
- Размер очереди заданий
- Бизнес-метрики (зоны, записи)

## 14. Развёртывание

### 14.1. Минимальные требования
- CPU: 2 ядра
- RAM: 512 MB
- Disk: 1 GB + место для зон

### 14.2. Порты
- **8080** - API (настраиваемый)
- **53** - DNS (BIND)
- **953** - RNDC (BIND)

### 14.3. Права доступа
- Чтение/запись: `/var/named/`, `/etc/named.conf`
- Пользователь: `named` для файлов зон
- Пользователь: `root:named` для конфигов

## 15. Версионирование файлов

- Хранение версий в PostgreSQL
- Поддержка rollback
- Проверка синтаксиса при откате
- Очистка старых версий

## 16. Аудит

Все операции записываются в таблицу `audit_logs`:
- Тип операции
- Зона/запись
- Статус (STARTED, COMPLETED, FAILED)
- Время выполнения
- Ошибки

---

**Дата последней актуализации:** 2026-09-09  
**Статус:** Production Ready