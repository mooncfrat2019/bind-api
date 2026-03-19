# 📘 BIND Manager API

**Версия:** 0.1.0 
**Последнее обновление:** Март 2026

---

## 📋 Содержание

1. [Обзор](#1-обзор)
2. [Архитектура](#2-архитектура)
3. [Быстрый старт](#3-быстрый-старт)
4. [Конфигурация](#4-конфигурация)
5. [API Reference](#5-api-reference)
6. [База данных](#6-база-данных)
7. [Безопасность](#7-безопасность)
8. [Мониторинг и отладка](#8-мониторинг-и-отладка)
9. [Troubleshooting](#9-troubleshooting)

---

## 1. Обзор

**BIND Manager API** — это REST API сервис для управления DNS-сервером BIND (named) на базе ОС РедОС/CentOS.

### Возможности

| Функция | Описание |
|---------|----------|
| ✅ Управление зонами | Создание, удаление, просмотр DNS-зон |
| ✅ Управление записями | Добавление/удаление A, AAAA, CNAME, MX, TXT, NS записей |
| ✅ Reverse DNS | Автоматическое создание PTR записей |
| ✅ Очередь заданий | Последовательная обработка для защиты от конфликтов |
| ✅ Аудит операций | Полное логирование всех изменений в PostgreSQL |
| ✅ Валидация | Проверка синтаксиса перед применением (`named-checkconf`, `named-checkzone`) |
| ✅ Serial management | Автоматическое увеличение Serial при изменениях |

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
│                        КЛИЕНТЫ (curl, UI)                       
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      GIN HTTP SERVER                            
│                    (порт 8080, REST API)                        
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                         HANDLERS                                
│  handleCreateZone, handleAddRecord, handleDeleteZone, etc.     
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      ОЧЕРЕДЬ ЗАДАНИЙ                            
│              chan *Job (in-memory, буфер 100)                   
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                      WORKER (1 горутина)                        
│              jobWorker() - обрабатывает последовательно          
└─────────────────────────────────────────────────────────────────┘
                               
              ┌───────────────┼───────────────┐
              ▼                 ▼                ▼
    ┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
    │  FILE LOCKS          BIND OPERATIONS       PostgreSQL    
    │  (sync.Mutex)        (rndc, named)         (Audit Log)   
    └─────────────────┘ └─────────────────┘ └─────────────────┘
```

### 2.2. Поток выполнения запроса

```
1. HTTP Request → Handler
2. Handler → Создаёт Job → Отправляет в channel
3. Handler → Ждёт ответа в ResponseCh (таймаут 30 сек)
4. Worker → Читает из channel → Выполняет операцию
5. Worker → Пишет аудит в PostgreSQL
6. Worker → Возвращает результат в ResponseCh
7. Handler → Возвращает ответ клиенту
```

### 2.3. Почему очередь in-memory, а не в БД?

| Характеристика | In-Memory Channel | Database Queue |
|----------------|-------------------|----------------|
| Производительность | ⚡ Микросекунды | 🐌 Миллисекунды |
| Персистентность | ❌ Теряется при рестарте | ✅ Сохраняется |
| Сложность | ✅ Минимальная | ⚠️ Требует polling |
| Для DNS | ✅ Достаточно | Избыточно |

DNS-изменения не критичны к потере при рестарте, поэтому используем простую in-memory очередь.

---

## 3. Быстрый старт

### 3.1. Требования

- Go 1.21+
- PostgreSQL 13+
- BIND 9.11+
- Root-доступ к серверу

### 3.2. Установка зависимостей

```bash
# Инициализация модуля
go mod init bind-api
go get -u github.com/gin-gonic/gin
go get -u gorm.io/gorm
go get -u gorm.io/driver/postgres
go get github.com/joho/godotenv
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
ALTER ROLE dns SET search_path TO bind_api;

# Выйти
\q
```

### 3.4. Настройка BIND

```bash
# Убедиться что named.conf включает доп. конфиг
echo 'include "/etc/named.zones.conf";' | sudo tee -a /etc/named.conf

# Создать файл для зон
sudo touch /etc/named.zones.conf
sudo chown root:named /etc/named.zones.conf
sudo chmod 640 /etc/named.zones.conf

# Проверить rndc
sudo rndc-confgen -a -c /etc/rndc.key
sudo chown named:named /etc/rndc.key
sudo chmod 640 /etc/rndc.key

# Перезапустить BIND
sudo systemctl restart named
```

### 3.5. Файл .env

```ini
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
```

### 3.6. Сборка и запуск

```bash
# Сборка
CGO_ENABLED=1 go build -o bind-api main.go

# Запуск
sudo ./bind-api

# Или как systemd сервис
sudo systemctl enable bind-api
sudo systemctl start bind-api
```

### 3.7. Проверка работы

```bash
# Проверить статус
curl http://localhost:8080/api/status | jq .

# Создать зону
curl -X POST http://localhost:8080/api/zone \
  -H "Content-Type: application/json" \
  -d '{"name": "test.local", "email": "admin.test.local"}' | jq .

# Добавить запись
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{"name": "www", "type": "A", "value": "192.168.1.100"}' | jq .

# Проверить аудит
curl http://localhost:8080/api/audit | jq .
```

---

## 4. Конфигурация

### 4.1. Переменные окружения

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
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
| `BIND_API_DB_URL` | — | Connection string (переопределяет всё) |
| `API_PORT` | `:8080` | Порт API |
| `GIN_MODE` | `release` | Режим Gin (debug/release) |

### 4.2. Приоритет конфигурации

```
1. BIND_API_DB_URL (если задан, игнорирует остальные DB_*)
2. Переменные окружения
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

| Метод | Эндпоинт | Описание |
|-------|----------|----------|
| `GET` | `/status` | Статус сервиса |
| `GET` | `/config` | Конфигурация API |
| `GET` | `/audit` | Журнал аудита |
| `GET` | `/audit/stats` | Статистика аудита |
| `POST` | `/reload` | Перезагрузка BIND |
| `GET` | `/zones` | Список зон |
| `POST` | `/zone` | Создание зоны |
| `GET` | `/zone/:name` | Информация о зоне |
| `DELETE` | `/zone/:name` | Удаление зоны |
| `POST` | `/zone/:name/record` | Добавление записи |
| `DELETE` | `/zone/:name/record/:record/:type` | Удаление записи |

### 5.3. Детальное описание

#### GET /status

**Описание:** Статус службы BIND и API.

**Ответ:**
```json
{
  "success": true,
  "message": "Статус сервиса",
  "data": {
    "named_status": "active",
    "api_version": "1.0.0",
    "queue_size": 0,
    "db_connected": true
  }
}
```

---

#### GET /config

**Описание:** Текущая конфигурация API.

**Ответ:**
```json
{
  "success": true,
  "message": "Текущая конфигурация",
  "data": {
    "zone_dir": "/var/named",
    "zone_conf": "/etc/named.zones.conf",
    "named_conf": "/etc/named.conf",
    "db_host": "localhost",
    "db_name": "dns",
    "db_schema": "bind_api",
    "default_ttl": 3600,
    "zones_found": 2,
    "queue_size": 0
  }
}
```

---

#### GET /audit

**Описание:** Журнал всех операций.

**Параметры запроса:**

| Параметр | Тип | Описание |
|----------|-----|----------|
| `zone` | query | Фильтр по зоне |
| `status` | query | Фильтр по статусу (STARTED/COMPLETED/FAILED) |
| `job_type` | query | Фильтр по типу операции |
| `limit` | query | Максимум записей (по умолчанию 100) |

**Пример:**
```bash
curl "http://localhost:8080/api/audit?zone=test.local&status=COMPLETED"
```

**Ответ:**
```json
{
  "success": true,
  "message": "Журнал аудита",
  "data": {
    "logs": [
      {
        "id": 1,
        "job_type": "CREATE_ZONE",
        "zone_name": "test.local",
        "status": "COMPLETED",
        "error": "",
        "created_at": "2026-03-19T19:55:33Z",
        "completed_at": "2026-03-19T19:55:35Z"
      }
    ]
  }
}
```

---

#### GET /audit/stats

**Описание:** Статистика операций.

**Ответ:**
```json
{
  "success": true,
  "message": "Статистика аудита",
  "data": {
    "total": 150,
    "completed": 145,
    "failed": 5,
    "success_rate": 96.67
  }
}
```

---

#### POST /zone

**Описание:** Создание новой DNS-зоны.

**Тело запроса:**

| Поле | Тип | Обязательное | Описание |
|------|-----|--------------|----------|
| `name` | string | ✅ | Имя зоны (test.local) |
| `email` | string | ❌ | Email админа (admin.test.local) |
| `type` | string | ❌ | forward/reverse (по умолчанию forward) |
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

---

#### GET /zones

**Описание:** Список всех зон.

**Ответ:**
```json
{
  "success": true,
  "message": "Список зон",
  "data": {
    "zones": [
      {
        "name": "test.local",
        "file": "/var/named/test.local.zone",
        "type": "forward",
        "config_file": "/etc/named.conf",
        "record_count": 5
      }
    ]
  }
}
```

---

#### GET /zone/:name

**Описание:** Информация о зоне и все записи.

**Ответ:**
```json
{
  "success": true,
  "message": "Информация о зоне",
  "data": {
    "name": "test.local",
    "type": "forward",
    "file": "/var/named/test.local.zone",
    "config_file": "/etc/named.conf",
    "record_count": 5,
    "records": [
      {"name": "@", "type": "SOA", "ttl": 3600, "value": "..."},
      {"name": "@", "type": "NS", "ttl": 3600, "value": "ns1.test.local."},
      {"name": "ns1", "type": "A", "ttl": 3600, "value": "10.69.13.3"},
      {"name": "www", "type": "A", "ttl": 3600, "value": "192.168.1.100"}
    ]
  }
}
```

---

#### DELETE /zone/:name

**Описание:** Удаление зоны и всех записей.

**Ответ:**
```json
{
  "success": true,
  "message": "Зона test.local удалена из /etc/named.conf",
  "data": {
    "config_file": "/etc/named.conf",
    "zone_file": "/var/named/test.local.zone"
  }
}
```

---

#### POST /zone/:name/record

**Описание:** Добавление DNS-записи.

**Тело запроса:**

| Поле | Тип | Обязательное | Описание |
|------|-----|--------------|----------|
| `name` | string | ✅ | Имя записи (www, @, mail) |
| `type` | string | ✅ | A/AAAA/CNAME/MX/TXT/NS |
| `value` | string | ✅ | Значение (IP, домен, текст) |
| `ttl` | int | ❌ | TTL в секундах (3600) |
| `reverse_ptr` | string | ❌ | Имя для PTR (только A/AAAA) |

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

---

#### DELETE /zone/:name/record/:record/:type

**Описание:** Удаление DNS-записи.

**Пример:**
```bash
curl -X DELETE http://localhost:8080/api/zone/test.local/record/www/A
```

**Примечание:** Для A/AAAA записей автоматически удаляется соответствующая PTR запись.

---

#### POST /reload

**Описание:** Принудительная перезагрузка BIND.

**Ответ:**
```json
{
  "success": true,
  "message": "BIND перезагружен"
}
```

---

## 6. База данных

### 6.1. Схема

```sql
CREATE TABLE audit_logs (
    id BIGSERIAL PRIMARY KEY,
    job_type VARCHAR(50) NOT NULL,
    zone_name VARCHAR(255),
    record_name VARCHAR(255),
    record_type VARCHAR(20),
    status VARCHAR(20) NOT NULL,
    error TEXT,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_audit_zone ON audit_logs(zone_name);
CREATE INDEX idx_audit_created ON audit_logs(created_at);
CREATE INDEX idx_audit_status ON audit_logs(status);
```

### 6.2. Статусы заданий

| Статус | Описание |
|--------|----------|
| `STARTED` | Задание начало выполнение |
| `COMPLETED` | Успешное завершение |
| `FAILED` | Ошибка выполнения |

### 6.3. Типы заданий

| Тип | Описание |
|-----|----------|
| `CREATE_ZONE` | Создание зоны |
| `DELETE_ZONE` | Удаление зоны |
| `ADD_RECORD` | Добавление записи |
| `DELETE_RECORD` | Удаление записи |
| `RELOAD` | Перезагрузка BIND |

---

## 7. Безопасность

### 7.1. Требования

| Требование | Рекомендация |
|------------|--------------|
| Запуск от | root (для доступа к /etc, /var/named) |
| Порт API | Закрыть фаерволом для внешних сетей |
| PostgreSQL | Ограничить доступ по IP |
| rndc ключ | 640 named:named |

### 7.2. Настройка фаервола

```bash
# Разрешить только локальный доступ
sudo firewall-cmd --add-port=8080/tcp --zone=internal --permanent
sudo firewall-cmd --reload

# Или ограничить по IP
sudo firewall-cmd --add-rich-rule='rule family="ipv4" source address="10.69.13.0/24" port port="8080" protocol="tcp" accept' --permanent
```

### 7.3. Рекомендации для продакшена

1. **HTTPS:** Использовать nginx как reverse proxy с SSL
2. **Авторизация:** Добавить Basic Auth или JWT
3. **Rate Limiting:** Ограничить количество запросов
4. **Аудит:** Включить логирование всех запросов
5. **Бэкапы:** Регулярный бэкап PostgreSQL и зон

---

## 8. Мониторинг и отладка

### 8.1. Логи приложения

```bash
# Журнал systemd
sudo journalctl -u bind-api -f

# Логи в реальном времени
sudo ./bind-api 2>&1 | tee /var/log/bind-api.log
```

### 8.2. Логи BIND

```bash
# Основные логи
sudo tail -f /var/log/messages | grep named

# Проверка синтаксиса
sudo named-checkconf
sudo named-checkzone test.local /var/named/test.local.zone

# Статус rndc
sudo rndc status
```

### 8.3. Проверка очереди

```bash
# Размер очереди
curl http://localhost:8080/api/status | jq '.data.queue_size'

# Статистика операций
curl http://localhost:8080/api/audit/stats | jq .

# Последние ошибки
curl "http://localhost:8080/api/audit?status=FAILED" | jq '.data.logs[:5]'
```

### 8.4. Проверка БД

```bash
# Подключиться
PGPASSWORD=password psql -h localhost -U dns -d dns

# Последние операции
SELECT * FROM audit_logs ORDER BY created_at DESC LIMIT 10;

# Ошибки за сегодня
SELECT * FROM audit_logs 
WHERE status = 'FAILED' 
AND created_at >= CURRENT_DATE;

# Статистика по зонам
SELECT zone_name, COUNT(*) as operations 
FROM audit_logs 
GROUP BY zone_name 
ORDER BY operations DESC;
```

---

## 9. Troubleshooting

### 9.1. Частые проблемы

| Проблема | Причина | Решение |
|----------|---------|---------|
| `permission denied` | Неправильные права на файлы | `chown named:named`, `chmod 644` |
| `rndc reload failed` | Проблемы с ключами rndc | `rndc-confgen -a`, проверить права |
| `schema does not exist` | Схема не создана в БД | `CREATE SCHEMA bind_api` |
| `queue is full` | Слишком много запросов | Увеличить `MaxQueueSize` или оптимизировать |
| `timeout` | Долгая операция | Проверить логи, увеличить `WorkerTimeout` |
| `SERVFAIL` | Ошибка в конфиге BIND | `named-checkconf`, проверить логи |
| `NXDOMAIN` | Зона не загрузилась | Проверить `named-checkzone`, права на файл |

### 9.2. Диагностика

```bash
# 1. Проверить права на конфиги
ls -la /etc/named.conf /etc/named.zones.conf

# 2. Проверить права на зоны
ls -la /var/named/*.zone

# 3. Проверить rndc
sudo rndc status

# 4. Проверить БД
PGPASSWORD=password psql -h localhost -U dns -d dns -c "SELECT version();"

# 5. Проверить очередь
curl http://localhost:8080/api/status | jq .

# 6. Проверить логи
sudo journalctl -u bind-api -n 50
```

### 9.3. Восстановление после сбоя

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

# 5. Перезапустить BIND
sudo systemctl restart named

# 6. Запустить API
sudo systemctl start bind-api
```

---

## 📞 Поддержка

При возникновении проблем:

1. Проверьте логи (`journalctl -u bind-api`)
2. Проверьте аудит (`/api/audit?status=FAILED`)
3. Проверьте синтаксис BIND (`named-checkconf`)
4. Проверьте права на файлы