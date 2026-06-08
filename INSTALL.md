# Инструкция по установке BIND API

## Системные требования

| Компонент | Минимальная версия | Рекомендуемая версия |
|-----------|-------------------|---------------------|
| **ОС** | РедОС 7.3 / CentOS 7+ / Ubuntu 20.04+ | РедОС 7.3 |
| **Go** | 1.21 | 1.21+ |
| **PostgreSQL** | 13 | 13+ |
| **BIND** | 9.11 | 9.16+ |
| **GCC** | — | для CGO (SQLite в тестах) |

---

## 1. Установка зависимостей

### 1.1. Установка Go

#### Для РедОС / CentOS / RHEL

```bash
# Скачивание Go
wget https://golang.org/dl/go1.21.5.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.21.5.linux-amd64.tar.gz

# Добавление в PATH
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
echo 'export GOPATH=$HOME/go' >> ~/.bashrc
source ~/.bashrc

# Проверка
go version
```

#### Для Ubuntu / Debian

```bash
sudo apt update
sudo apt install -y golang-1.21
# или
sudo snap install go --classic
```

### 1.2. Установка PostgreSQL

```bash
# РедОС / CentOS
sudo yum install -y postgresql13-server postgresql13-contrib
sudo /usr/pgsql-13/bin/postgresql-13-setup initdb
sudo systemctl enable postgresql-13
sudo systemctl start postgresql-13

# Ubuntu / Debian
sudo apt install -y postgresql-13 postgresql-contrib-13
sudo systemctl enable postgresql
sudo systemctl start postgresql
```

### 1.3. Установка BIND

```bash
# РедОС / CentOS
sudo yum install -y bind bind-utils

# Ubuntu / Debian
sudo apt install -y bind9 bind9utils dnsutils

# Запуск BIND
sudo systemctl enable named
sudo systemctl start named
```

### 1.4. Установка GCC (для сборки с CGO)

```bash
# РедОС / CentOS
sudo yum install -y gcc gcc-c++ make

# Ubuntu / Debian
sudo apt install -y build-essential
```

### 1.5. Установка Git

```bash
sudo yum install -y git   # РедОС/CentOS
sudo apt install -y git   # Ubuntu/Debian
```

---

## 2. Настройка PostgreSQL

### 2.1. Создание базы данных и пользователя

```bash
sudo -u postgres psql
```

```sql
-- Создание пользователя
CREATE USER dns WITH PASSWORD 'your_secure_password_here';

-- Создание базы данных
CREATE DATABASE dns OWNER dns;

-- Подключение к базе
\c dns

-- Создание схемы
CREATE SCHEMA IF NOT EXISTS bind_api;

-- Назначение прав
GRANT ALL PRIVILEGES ON SCHEMA bind_api TO dns;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA bind_api TO dns;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA bind_api TO dns;

-- Настройка прав по умолчанию
ALTER DEFAULT PRIVILEGES IN SCHEMA bind_api GRANT ALL ON TABLES TO dns;
ALTER DEFAULT PRIVILEGES IN SCHEMA bind_api GRANT ALL ON SEQUENCES TO dns;

\q
```

### 2.2. Настройка аутентификации PostgreSQL

Отредактируйте файл `pg_hba.conf`:

```bash
# РедОС / CentOS
sudo nano /var/lib/pgsql/13/data/pg_hba.conf

# Ubuntu / Debian
sudo nano /etc/postgresql/13/main/pg_hba.conf
```

Добавьте или измените строки:

```conf
# TYPE  DATABASE    USER    ADDRESS         METHOD
local   all         all                     trust
host    all         all     127.0.0.1/32    scram-sha-256
host    all         all     ::1/128         scram-sha-256
```

Перезапустите PostgreSQL:

```bash
sudo systemctl restart postgresql-13   # РедОС/CentOS
sudo systemctl restart postgresql      # Ubuntu/Debian
```

---

## 3. Настройка BIND

### 3.1. Создание файлов конфигурации

```bash
# Основной конфигурационный файл для зон
sudo touch /etc/named.zones.conf
sudo chown root:named /etc/named.zones.conf
sudo chmod 640 /etc/named.zones.conf

# Директория для файлов зон
sudo mkdir -p /var/named
sudo chown named:named /var/named
sudo chmod 755 /var/named
```

### 3.2. Настройка named.conf

```bash
sudo nano /etc/named.conf
```

Приведите файл к следующему виду:

```bind
options {
    listen-on port 53 { any; };
    listen-on-v6 port 53 { any; };
    directory "/var/named";
    dump-file "/var/named/data/cache_dump.db";
    statistics-file "/var/named/data/named_stats.txt";
    memstatistics-file "/var/named/data/named_mem_stats.txt";
    allow-query { any; };
    recursion no;                    # Отключаем рекурсию для authoritative сервера
    
    # Разрешение трансферы для реплик
    allow-transfer { 
        10.50.13.4;                 # IP вашей реплики
        localhost; 
    };
    
    also-notify { 
        10.50.13.4;                 # IP вашей реплики
    };
    
    dnssec-validation no;
    
    pid-file "/run/named/named.pid";
    session-keyfile "/run/named/session.key";
};

logging {
    channel default_debug {
        file "data/named.run";
        severity dynamic;
    };
};

zone "." IN {
    type hint;
    file "named.ca";
};

include "/etc/named.zones.conf";
include "/etc/named.rfc1912.zones";
include "/etc/named.root.key";
```
### 3.3. Настройка rndc ключа

#### 3.3.1 Автоматическая генерация ключа

```bash
# Генерация ключа rndc
sudo rndc-confgen -a -c /etc/rndc.key
sudo chown root:named /etc/rndc.key
sudo chmod 640 /etc/rndc.key

# Проверка созданного ключа
sudo cat /etc/rndc.key
```
#### 3.3.2 Ручная настройка rndc (если автоматическая не работает)

Создайте файл `/etc/rndc.key`:

```bash
sudo nano /etc/rndc.key
```

Добавьте содержимое:

```bind
key "rndc-key" {
    algorithm hmac-sha256;
    secret "pJ5sNk7xR9tF2gH4mK6pL8qW1eR3tY5uI7oA0sD2fG4hJ6=";
};
```

>**Важно:** Сгенерируйте свой уникальный секрет:

```bash
# Генерация случайного ключа
echo "secret \"$(openssl rand -base64 32)\";"
```
#### 3.3.3 Добавление controls в named.conf

Отредактируйте `/etc/named.conf`:
```bash
sudo nano /etc/named.conf
```
Добавьте секцию `controls` (вне секции `options`):
```bind
controls {
    inet 127.0.0.1 port 953 allow { 127.0.0.1; } keys { "rndc-key"; };
};
```
#### 3.3.4 Подключение ключа в named.conf

Убедитесь, что в конце `named.conf` есть строка:

```bind
include "/etc/rndc.key";
```
#### 3.3.5 Полный пример /etc/named.conf
```bind
options {
    listen-on port 53 { any; };
    listen-on-v6 port 53 { any; };
    directory "/var/named";
    allow-query { any; };
    recursion no;
    allow-transfer { 
        10.50.13.4;      # IP реплики
        localhost; 
    };
    also-notify { 
        10.50.13.4;      # IP реплики
    };
    pid-file "/run/named/named.pid";
};

controls {
    inet 127.0.0.1 port 953 allow { 127.0.0.1; } keys { "rndc-key"; };
};

zone "." IN {
    type hint;
    file "named.ca";
};

include "/etc/named.zones.conf";
include "/etc/rndc.key";
```
#### 3.3.6 Проверка работы rndc
```bash
# Проверка синтаксиса конфигурации
sudo named-checkconf

# Проверка статуса rndc
sudo rndc status

# Ожидаемый вывод:
# version: 9.11.4-P2-RedHat-9.11.4-26.P2.el7_9.13
# ...

# Проверка конкретной зоны
sudo rndc zonestatus example.com

# Проверка возможности reload
sudo rndc reload
```
#### 3.3.7 Устранение ошибок rndc

**Ошибка:** `rndc: neither /etc/rndc.conf nor /etc/rndc.key was found`

**Решение:**
```bash
sudo rndc-confgen -a -c /etc/rndc.key
sudo chown root:named /etc/rndc.key
sudo chmod 640 /etc/rndc.key
sudo systemctl restart named
```
**Ошибка:** `rndc: connection to remote host closed`

**Решение:**
1. Проверьте, что в `named.conf` есть секция `controls`
2. Проверьте, что порт 953 слушается:
   ```bash
   sudo netstat -tlnp | grep 953
   ```
3. Проверьте SELinux (если включен):
   ```bash
   sudo setsebool -P named_tcp_bind_http_port_t on
   ```

**Ошибка:** `rndc: 'reload' failed: permission denied`

**Решение:**
```bash
# Проверьте права на ключ
sudo ls -la /etc/rndc.key
# Должно быть: -rw-r----- 1 root named

# Исправление прав
sudo chown root:named /etc/rndc.key
sudo chmod 640 /etc/rndc.key
sudo systemctl restart named
```
#### 3.3.8 Настройка rndc для удаленного управления (опционально)

Если нужно управлять BIND с другого сервера:

```bash
# На мастере создайте ключ с именем хоста реплики
sudo rndc-confgen -a -c /etc/rndc.key -t replication-key
```

В `named.conf` добавьте:

```bind
controls {
    inet 0.0.0.0 port 953 allow { 
        10.50.13.4;        # IP реплики
        127.0.0.1; 
    } keys { "rndc-key"; };
};
```
### 3.4 Проверка конфигурации BIND
```bash
# Проверка синтаксиса всех конфигов
sudo named-checkconf

# Перезапуск BIND
sudo systemctl restart named

# Проверка статуса
sudo systemctl status named

# Проверка rndc
sudo rndc status
sudo rndc reload
```

---

## 4. Установка BIND Manager API

### 4.1. Получение бинарных файлов

Если нет возможности сборки из исходников, можно взять бинарные файлы из [релизов](https://github.com/mooncfrat2019/bind-api/releases).

### 4.2. Клонирование репозитория
```bash
git clone https://github.com/mooncfrat2019/bind-api.git
cd bind-api
```
### 4.3. Установка зависимостей
```bash
go mod download
go mod tidy
```
### 4.4. Сборка приложения
```bash
# Для мастера (с поддержкой PostgreSQL)
CGO_ENABLED=1 go build -o bind-api main.go

# Для проверки (если не нужен CGO)
CGO_ENABLED=0 go build -o bind-api main.go

#На старых РедОС (7.3) лучше использовать второй вариант
```
### 4.5. Создание конфигурационного файла .env
```bash
cp .env.example .env
nano .env
```
#### Настройка для MASTER сервера:
```env
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
BIND_API_DB_PASSWORD=your_secure_password_here
BIND_API_DB_NAME=dns
BIND_API_DB_SSLMODE=disable
BIND_API_DB_SCHEMA=bind_api

# API настройки
API_PORT=:8080
GIN_MODE=release

# Токен для синхронизации (обязательно измените!)
SYNC_API_TOKEN=your_secure_sync_token_12345

# Ограничение доступа к sync API по подсети (CIDR)
# Если не указано - разрешены все IP
SYNC_API_SUBNET=10.0.0.0/8

# Bootstrap API-ключ для первого запуска (опционально)
# Должен быть от 32 до 120 символов
# Создаётся только если таблица api_keys пуста
# Срок действия: 7 дней, права: *
BIND_API_BOOTSTRAP_KEY=your-very-secure-bootstrap-key-min-32-chars

# Уровень логирования (DEBUG, INFO, WARN, ERROR)
LOG_LEVEL=INFO

# Настройки очереди заданий
MAX_QUEUE_SIZE=1000
WORKER_TIMEOUT=30
BATCH_SIZE=10
BATCH_INTERVAL=5
QUEUE_THRESHOLD_LOW=0.3
QUEUE_THRESHOLD_HIGH=0.8
RELOAD_INTERVAL=10

# Интервал очистки sync_states от удалённых зон
SYNC_CLEANUP_INTERVAL=5m
```
#### Настройка для REPLICA сервера:

```env
# Роль сервера
APP_ROLE=replica

# BIND настройки
BIND_ZONE_DIR=/var/named/
BIND_NAMED_CONF=/etc/named.conf
BIND_ZONE_CONF=/etc/named.zones.conf

# API настройки
API_PORT=:8080

# Мастер сервер
MASTER_URL=http://10.50.13.3:8080
MASTER_API_TOKEN=your_secure_sync_token_12345

# Интервал синхронизации (сек)
SYNC_INTERVAL=30

# Трансформация конфигурации
REPLICA_MASTER_IP=10.50.13.3
REPLICA_ZONE_TYPE=slave
REPLICA_ZONE_SUBDIR=slaves
REPLICA_REMOVE_ALLOW_TRANSFER=true
REPLICA_DISABLE_IPV6=true

# Внешний IP реплики для проверки резолвинга
REPLICA_EXTERNAL_IP=10.50.13.4

# Только для dev/test!
ALLOW_INSECURE_SYNC=true

# Уровень логирования
LOG_LEVEL=INFO
```

---

## 5. Запуск сервиса

### 5.1. Создание systemd сервиса
```
bash
sudo nano /etc/systemd/system/bind-api.service
```

```
ini
[Unit]
Description=BIND Manager API
After=network.target postgresql-13.service named.service
Wants=postgresql-13.service named.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/bind-api
ExecStart=/opt/bind-api/bind-api
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal
Environment="LOG_LEVEL=INFO"

[Install]
WantedBy=multi-user.target
```
### 5.2. Установка и запуск
```bash
# Копирование бинарника и конфига
sudo mkdir -p /opt/bind-api
sudo cp bind-api /opt/bind-api/
sudo cp .env /opt/bind-api/
sudo chmod +x /opt/bind-api/bind-api

# Перезагрузка systemd
sudo systemctl daemon-reload

# Запуск сервиса
sudo systemctl enable bind-api
sudo systemctl start bind-api

# Проверка статуса
sudo systemctl status bind-api
```

### 5.3. Проверка логов

```bash
# Журнал сервиса
sudo journalctl -u bind-api -f

# Логи приложения
sudo journalctl -u bind-api -n 100
```

---

## 6. Настройка прав доступа

### 6.1. Права на файлы конфигурации BIND

```bash
# Права на конфиги BIND
sudo chown root:named /etc/named.conf
sudo chmod 640 /etc/named.conf

sudo chown root:named /etc/named.zones.conf
sudo chmod 640 /etc/named.zones.conf

# Права на директорию зон
sudo chown named:named /var/named
sudo chmod 755 /var/named
```

### 6.2. Права на директорию логов

```bash
sudo mkdir -p /var/log/bind-api
sudo chown root:root /var/log/bind-api
sudo chmod 755 /var/log/bind-api
```

---

## 7. Проверка работоспособности

### 7.1. Проверка статуса API

```bash
curl http://localhost:8080/api/status
```

Ожидаемый ответ:

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

### 7.2. Проверка bootstrap-ключа

Если вы задали `BIND_API_BOOTSTRAP_KEY`, проверьте логи:

```bash
sudo journalctl -u bind-api -n 50 | grep -i "bootstrap"
```

Пример вывода:
```
[INFO] Bootstrap API-ключ создан из переменной окружения BIND_API_BOOTSTRAP_KEY со сроком действия 7 дней
```

### 7.3. Проверка работы API

```bash
# Если использовали bootstrap-ключ
API_KEY="your-bootstrap-key-value"

# Список зон
curl -H "X-API-Key: $API_KEY" http://localhost:8080/api/read/zones
```


### 7.4. Создание постоянного API-ключа

```bash
curl -X POST http://localhost:8080/api/keys \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{
    "name": "admin-key",
    "description": "Постоянный административный ключ",
    "permissions": ["*"],
    "expires_in": 0
  }'
```


Сохраните полученный ключ и отзовите bootstrap-ключ:

```bash
curl -X DELETE http://localhost:8080/api/keys/1 \
  -H "X-API-Key: $API_KEY"
```


### 7.5. Проверка health check

```bash
curl http://localhost:8080/health
```


Ожидаемый ответ:
```json
{
  "status": "healthy",
  "timestamp": "2026-06-08T10:30:00Z"
}
```


### 7.6. Проверка Prometheus-метрик

```bash
curl http://localhost:8080/metrics
```


---

## 8. Установка реплики

### 8.1. Настройка BIND на реплике

```bash
# На реплике, создание директории для slave-зон
sudo mkdir -p /var/named/slaves
sudo chown named:named /var/named/slaves
sudo chmod 755 /var/named/slaves
```


### 8.2. Настройка named.conf на реплике

```bash
sudo nano /etc/named.conf
```

```bind
options {
    listen-on port 53 { any; };
    listen-on-v6 port 53 { any; };
    directory "/var/named";
    allow-query { any; };
    recursion no;
    dnssec-validation no;
    
    # Отключаем уведомления
    also-notify { };
    
    pid-file "/run/named/named.pid";
};

zone "." IN {
    type hint;
    file "named.ca";
};

include "/etc/named.zones.conf";
```

### 8.3. Запуск API на реплике

Повторите шаги 4-5 на реплике, используя конфигурацию для REPLICA.

---

## 9. Устранение неполадок

### 9.1. Ошибка подключения к БД

```bash
# Проверка PostgreSQL
sudo systemctl status postgresql-13
sudo -u postgres psql -c "\l"

# Проверка пароля
PGPASSWORD=your_password psql -h localhost -U dns -d dns -c "SELECT 1"
```

### 9.2. Ошибка прав доступа к файлам BIND

```bash
# Проверка прав
ls -la /etc/named.conf
ls -la /var/named/

# Исправление прав
sudo chown root:named /etc/named.conf
sudo chmod 640 /etc/named.conf
sudo chown named:named /var/named/*.zone
sudo chmod 644 /var/named/*.zone
```

### 9.3. Ошибка rndc

```bash
# Проверка rndc
sudo rndc status

# Перегенерация ключа
sudo rndc-confgen -a -c /etc/rndc.key
sudo chown named:named /etc/rndc.key
sudo systemctl restart named
```

### 9.4. API не отвечает

```bash
# Проверка что порт слушается
sudo netstat -tlnp | grep 8080

# Проверка логов
sudo journalctl -u bind-api -n 50

# Проверка конфигурации
cat /opt/bind-api/.env
```

### 9.5. Проблемы с rndc после установки

```bash
# Полная перегенерация rndc конфигурации
sudo rm -f /etc/rndc.key /etc/rndc.conf
sudo rndc-confgen -a -c /etc/rndc.key
sudo chown root:named /etc/rndc.key
sudo chmod 640 /etc/rndc.key

# Проверка что ключ подключен в named.conf
grep "include.*rndc.key" /etc/named.conf

# Перезапуск BIND
sudo systemctl restart named

# Тест
sudo rndc status
```

### 9.6. Bootstrap-ключ не создан

**Проверьте:**
1. Таблица `api_keys` пуста:
```bash
PGPASSWORD=password psql -h localhost -U dns -d dns -c "SELECT COUNT(*) FROM bind_api.api_keys"
```

2. Длина ключа от 32 до 120 символов
3. Переменная `BIND_API_BOOTSTRAP_KEY` задана в `.env` или экспортирована

### 9.7. IP заблокирован после неудачных попыток

**Проверьте логи:**
```bash
sudo journalctl -u bind-api | grep "заблокирован"
```


**Решение:**
1. Подождать 15 минут (автоматическая разблокировка)
2. Или перезапустить сервис: `sudo systemctl restart bind-api`

### 9.8. Ошибки синхронизации реплики

**Проверьте:**
```bash
# На реплике
sudo journalctl -u bind-api -f | grep -E "(синхронизация|retransfer|зон)"

# Проверка доступности мастера
curl -H "X-Sync-Token: <токен>" http://<master-ip>:8080/api/sync/state
```


---

## 10. Обновление

```bash
# Остановка сервиса
sudo systemctl stop bind-api

# Обновление кода
cd /opt/bind-api
git pull

# Пересборка
go mod download
CGO_ENABLED=1 go build -o bind-api main.go

# Или скачайте бинарные файлы из релиза
wget https://github.com/mooncfrat2019/bind-api/releases/download/v0.5.0/bind-api
chmod +x bind-api
sudo mv bind-api /opt/bind-api/

# Запуск
sudo systemctl start bind-api
```
---

## 11. Безопасность

### 11.1. Рекомендации для production

- ✅ Используйте HTTPS для `MASTER_URL`
- ✅ Ограничьте доступ к `/api/sync/*` по IP (firewall)
- ✅ Используйте сложные bootstrap токены (минимум 32 символа)
- ✅ Настройте `SYNC_API_SUBNET` для ограничения доступа
- ✅ Регулярно обновляйте API-ключи
- ✅ Настройте мониторинг метрик
- ✅ Используйте `LOG_LEVEL=INFO` или `WARN` в production

### 11.2. Запрещено в production

- ❌ `ALLOW_INSECURE_SYNC=true`
- ❌ Простые пароли и токены
- ❌ Открытый доступ к sync API
- ❌ Bootstrap-ключи с неограниченным сроком
- ❌ `LOG_LEVEL=DEBUG` (может раскрыть чувствительные данные и забить логами)

### 11.3. Настройка firewall

```bash
# Разрешить только необходимые порты
sudo firewall-cmd --permanent --add-port=8080/tcp    # API
sudo firewall-cmd --permanent --add-port=53/udp      # DNS
sudo firewall-cmd --permanent --add-port=53/tcp      # DNS
sudo firewall-cmd --permanent --add-port=953/tcp     # RNDC

# Ограничить доступ к sync API по IP
sudo firewall-cmd --permanent --add-rich-rule='
  rule family="ipv4" source address="10.0.0.0/8" 
  port port="8080" protocol="tcp" accept'
```


---

## 12. Фоновые процессы

На MASTER сервере автоматически запускаются следующие фоновые процессы:

| Процесс | Интервал | Назначение |
|---------|----------|------------|
| **Очистка попыток авторизации** | 5 мин | Удаление старых записей о неудачных попытках входа |
| **Мониторинг named.conf** | 30 сек | Отслеживание изменений в конфигурации BIND |
| **Очистка orphan зон** | 5 мин | Удаление записей о несуществующих зонах из БД |

Все процессы запускаются автоматически при старте приложения в роли `master`.

### 12.1. Настройка интервалов

```env
# Интервал очистки sync_states (по умолчанию 5m)
SYNC_CLEANUP_INTERVAL=10m

# Интервал очистки попыток авторизации (жёстко задан 5 мин)
# Изменяется в коде

# Интервал мониторинга named.conf (жёстко задан 30 сек)
# Изменяется в коде
```


---

## 13. Мониторинг

### 13.1. Prometheus-метрики

Эндпоинт `/metrics` возвращает метрики в формате Prometheus:

```bash
curl http://localhost:8080/metrics
```


**Доступные метрики:**

| Метрика | Описание |
|---------|----------|
| `bind_api_operations_total` | Всего операций (с лейблами type, status) |
| `bind_api_operations_duration_seconds` | Длительность операций (histogram) |
| `bind_api_queue_size` | Текущий размер очереди заданий |
| `bind_api_queue_mode` | Текущий режим очереди (0=normal, 1=batch) |
| `bind_api_zones_total` | Количество зон |
| `bind_api_records_total` | Количество записей |
| `bind_api_replica_checks_total` | Проверки реплик (с лейблом resolved) |
| `bind_api_replica_retransfers_total` | Операции retransfer |
| `bind_api_auth_attempts_total` | Попытки авторизации (с лейблом success) |

### 13.2. Интеграция с Prometheus

**Пример scrape_config:**
```yaml
scrape_configs:
  - job_name: 'bind-api'
    static_configs:
      - targets: ['localhost:8080']
    metrics_path: '/metrics'
```


### 13.3. Проверка очереди

```bash
curl http://localhost:8080/api/status | jq '.data.queue_size'
```


---

## 14. Переменные окружения (полный список)

### Основные

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `APP_ROLE` | `master` | Роль сервера: `master` или `replica` |
| `LOG_LEVEL` | `INFO` | Уровень логирования: DEBUG, INFO, WARN, ERROR |

### BIND

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `BIND_ZONE_DIR` | `/var/named/` | Директория для файлов зон |
| `BIND_NAMED_CONF` | `/etc/named.conf` | Основной конфиг BIND |
| `BIND_ZONE_CONF` | `/etc/named.zones.conf` | Доп. файл для зон |

### PostgreSQL

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `BIND_API_DB_URL` | — | Полный DSN (приоритет над остальными) |
| `BIND_API_DB_HOST` | `localhost` | Хост PostgreSQL |
| `BIND_API_DB_PORT` | `5432` | Порт PostgreSQL |
| `BIND_API_DB_USER` | `bindapi` | Пользователь БД |
| `BIND_API_DB_PASSWORD` | — | Пароль БД |
| `BIND_API_DB_NAME` | `bind_api` | Имя базы данных |
| `BIND_API_DB_SSLMODE` | `disable` | SSL режим |
| `BIND_API_DB_SCHEMA` | `public` | Схема для таблиц |

### API

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `API_PORT` | `:8080` | Порт API |
| `GIN_MODE` | `release` | Режим Gin |

### Синхронизация (MASTER)

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `SYNC_API_TOKEN` | — | Токен для синхронизации (мин. 32 символа) |
| `SYNC_API_SUBNET` | — | Ограничение доступа к sync API по подсети (CIDR) |
| `BIND_API_BOOTSTRAP_KEY` | — | Bootstrap API-ключ (32-120 символов) |

### Синхронизация (REPLICA)

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `MASTER_URL` | — | URL мастера |
| `MASTER_API_TOKEN` | — | Токен для подключения к мастеру |
| `SYNC_INTERVAL` | `30` | Интервал опроса мастера (сек) |
| `REPLICA_MASTER_IP` | `127.0.0.1` | IP мастера для `masters {}` |
| `REPLICA_ZONE_TYPE` | `slave` | Тип зон на реплике |
| `REPLICA_ZONE_SUBDIR` | `slaves` | Подкаталог для slave-файлов |
| `REPLICA_REMOVE_ALLOW_TRANSFER` | `false` | Удалять `allow-transfer` при трансформации |
| `REPLICA_DISABLE_IPV6` | `false` | Отключать `listen-on-v6` на реплике |
| `REPLICA_EXTERNAL_IP` | `127.0.0.1` | IP реплики для проверки резолвинга |
| `ALLOW_INSECURE_SYNC` | `false` | Разрешить HTTP для sync API (dev/test) |
| `MASTER_API_PORT` | `8080` | Порт API мастера для fallback |

### Очередь заданий (MASTER)

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `MAX_QUEUE_SIZE` | `1000` | Максимальный размер очереди |
| `WORKER_TIMEOUT` | `30` | Таймаут выполнения задания (сек) |
| `BATCH_SIZE` | `10` | Размер пакета для batch-режима |
| `BATCH_INTERVAL` | `5` | Интервал сброса пакета (сек) |
| `QUEUE_THRESHOLD_LOW` | `0.3` | Порог переключения в normal режим |
| `QUEUE_THRESHOLD_HIGH` | `0.8` | Порог переключения в batch режим |
| `RELOAD_INTERVAL` | `10` | Интервал reload в batch режиме (сек) |

### Фоновые процессы (MASTER)

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `SYNC_CLEANUP_INTERVAL` | `5m` | Интервал очистки sync_states |

---

**© 2026 BIND API | Версия 0.4.10**