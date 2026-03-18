# API BIND Manager

## 📋 Содержание

| Метод | Эндпоинт | Описание |
|-------|----------|----------|
| `GET` | `/api/status` | Статус службы BIND |
| `GET` | `/api/config` | Конфигурация API |
| `POST` | `/api/reload` | Перезагрузка BIND |
| `GET` | `/api/zones` | Список всех зон |
| `POST` | `/api/zone` | Создание зоны |
| `GET` | `/api/zone/:name` | Информация о зоне |
| `DELETE` | `/api/zone/:name` | Удаление зоны |
| `POST` | `/api/zone/:name/record` | Добавление записи |
| `DELETE` | `/api/zone/:name/record/:record/:type` | Удаление записи |

---

## 1. Проверка статуса службы BIND

**Метод:** `GET`  
**URL:** `/api/status`

**Описание:** Возвращает текущий статус службы `named`.

**Пример запроса:**
```bash
curl http://localhost:8080/api/status
```

**Ответ (200 OK):**
```json
{
  "success": true,
  "message": "Статус сервиса",
  "data": {
    "named_status": "active",
    "api_version": "1.0.0"
  }
}
```

---

## 2. Получение конфигурации API

**Метод:** `GET`  
**URL:** `/api/config`

**Описание:** Возвращает текущие настройки API и список найденных зон.

**Пример запроса:**
```bash
curl http://localhost:8080/api/config
```

**Ответ (200 OK):**
```json
{
  "success": true,
  "message": "Текущая конфигурация",
  "data": {
    "zone_dir": "/var/named",
    "zone_conf": "/etc/named.zones.conf",
    "named_conf": "/etc/named.conf",
    "default_ttl": 3600,
    "api_port": ":8080",
    "gin_mode": "release",
    "running_as": 0,
    "go_version": "1.21.0",
    "zones_found": 2,
    "zones": [
      {
        "name": "vk.local",
        "file": "/var/named/vk.local.zone",
        "type": "forward",
        "config_file": "/etc/named.conf"
      }
    ]
  }
}
```

---

## 3. Перезагрузка конфигурации BIND

**Метод:** `POST`  
**URL:** `/api/reload`

**Описание:** Принудительно применяет изменения через `rndc reload`.

**Пример запроса:**
```bash
curl -X POST http://localhost:8080/api/reload
```

**Ответ (200 OK):**
```json
{
  "success": true,
  "message": "BIND перезагружен",
  "data": null
}
```

---

## 4. Список всех зон

**Метод:** `GET`  
**URL:** `/api/zones`

**Описание:** Возвращает все зарегистрированные зоны с информацией о файлах и количестве записей.

**Пример запроса:**
```bash
curl http://localhost:8080/api/zones
```

**Ответ (200 OK):**
```json
{
  "success": true,
  "message": "Список зон",
  "data": {
    "zones": [
      {
        "name": "vk.local",
        "file": "/var/named/vk.local.zone",
        "type": "forward",
        "config_file": "/etc/named.conf",
        "record_count": 5
      },
      {
        "name": "13.69.100.in-addr.arpa",
        "file": "/var/named/vk.local.rev",
        "type": "reverse",
        "config_file": "/etc/named.conf",
        "record_count": 3
      }
    ]
  }
}
```

---

## 5. Создание новой зоны

**Метод:** `POST`  
**URL:** `/api/zone`

**Описание:** Создаёт новую мастер-зону с базовой SOA, NS и A записями для ns1.

**Параметры запроса:**

| Поле | Тип | Обязательное | Описание |
|------|-----|--------------|----------|
| `name` | string | ✅ | Имя зоны (например, `example.com`) |
| `email` | string | ❌ | Email администратора (по умолчанию `admin.<name>`) |
| `type` | string | ❌ | Тип зоны: `forward` или `reverse` (по умолчанию `forward`) |
| `ns_ip` | string | ❌ | IP адрес для ns1 (по умолчанию берётся с сервера) |
| `config_file` | string | ❌ | Путь к конфигу (по умолчанию используется существующий) |

**Пример запроса (базовый):**
```bash
curl -X POST http://localhost:8080/api/zone \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test.local",
    "email": "admin.test.local"
  }'
```

**Пример запроса (с указанием IP):**
```bash
curl -X POST http://localhost:8080/api/zone \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test.local",
    "email": "admin.test.local",
    "ns_ip": "10.69.13.3"
  }'
```

**Пример запроса (обратная зона):**
```bash
curl -X POST http://localhost:8080/api/zone \
  -H "Content-Type: application/json" \
  -d '{
    "name": "13.69.100.in-addr.arpa",
    "type": "reverse",
    "email": "admin.13.69.100.in-addr.arpa"
  }'
```

**Ответ (200 OK):**
```json
{
  "success": true,
  "message": "Зона test.local (forward) создана в /etc/named.conf",
  "data": {
    "zone_file": "/var/named/test.local.zone",
    "config_file": "/etc/named.conf",
    "ns1_ips": ["10.69.13.3"]
  }
}
```

**Ответ (409 Conflict — зона существует):**
```json
{
  "success": false,
  "message": "Зона уже существует в конфигурации",
  "data": null
}
```

---

## 6. Получение информации о зоне

**Метод:** `GET`  
**URL:** `/api/zone/:name`

**Описание:** Возвращает все записи указанной зоны.

**Параметры URL:**

| Параметр | Описание |
|----------|----------|
| `:name` | Имя зоны (например, `test.local`) |

**Пример запроса:**
```bash
curl http://localhost:8080/api/zone/test.local
```

**Ответ (200 OK):**
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
      {
        "name": "@",
        "type": "SOA",
        "ttl": 3600,
        "value": "ns1.test.local. admin.test.local. 2024010101 3600 600 604800 3600"
      },
      {
        "name": "@",
        "type": "NS",
        "ttl": 3600,
        "value": "ns1.test.local."
      },
      {
        "name": "ns1",
        "type": "A",
        "ttl": 3600,
        "value": "10.69.13.3"
      },
      {
        "name": "@",
        "type": "A",
        "ttl": 3600,
        "value": "10.69.13.3"
      },
      {
        "name": "www",
        "type": "A",
        "ttl": 3600,
        "value": "10.69.13.100"
      }
    ]
  }
}
```

**Ответ (404 Not Found):**
```json
{
  "success": false,
  "message": "Зона не найдена в конфигурации",
  "data": null
}
```

---

## 7. Удаление зоны

**Метод:** `DELETE`  
**URL:** `/api/zone/:name`

**Описание:** Удаляет файл зоны и её объявление из конфигурации.

**Параметры URL:**

| Параметр | Описание |
|----------|----------|
| `:name` | Имя зоны (например, `test.local`) |

**Пример запроса:**
```bash
curl -X DELETE http://localhost:8080/api/zone/test.local
```

**Ответ (200 OK):**
```json
{
  "success": true,
  "message": "Зона test.local удалена из /etc/named.conf",
  "data": {
    "config_file": "/etc/named.conf",
    "zone_file": "/var/named/test.local.zone",
    "file_existed": true
  }
}
```

**Ответ (404 Not Found):**
```json
{
  "success": false,
  "message": "Зона не найдена в конфигурации",
  "data": null
}
```

---

## 8. Добавление записи в зону

**Метод:** `POST`  
**URL:** `/api/zone/:name/record`

**Описание:** Добавляет новую DNS-запись в указанную зону. Для A/AAAA записей можно автоматически создать PTR.

**Параметры URL:**

| Параметр | Описание |
|----------|----------|
| `:name` | Имя зоны (например, `test.local`) |

**Параметры запроса:**

| Поле | Тип | Обязательное | Описание |
|------|-----|--------------|----------|
| `name` | string | ✅ | Имя записи (`www`, `@`, `mail` и т.д.) |
| `type` | string | ✅ | Тип записи (`A`, `AAAA`, `CNAME`, `MX`, `TXT`, `NS`) |
| `value` | string | ✅ | Значение записи (IP, домен, текст) |
| `ttl` | int | ❌ | TTL в секундах (по умолчанию 3600) |
| `reverse_ptr` | string | ❌ | Имя для PTR записи (только для A/AAAA) |

**Примеры запросов:**

**A запись (IPv4):**
```bash
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{
    "name": "www",
    "type": "A",
    "value": "10.69.13.100",
    "ttl": 3600
  }'
```

**A запись с автоматической PTR:**
```bash
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{
    "name": "blog",
    "type": "A",
    "value": "10.69.13.101",
    "reverse_ptr": "blog.test.local"
  }'
```

**AAAA запись (IPv6):**
```bash
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{
    "name": "ipv6",
    "type": "AAAA",
    "value": "2001:db8::1"
  }'
```

**CNAME запись:**
```bash
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{
    "name": "shop",
    "type": "CNAME",
    "value": "www.test.local"
  }'
```

**MX запись (формат: "приоритет сервер"):**
```bash
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{
    "name": "@",
    "type": "MX",
    "value": "10 mail.test.local"
  }'
```

**TXT запись (например, SPF):**
```bash
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{
    "name": "@",
    "type": "TXT",
    "value": "v=spf1 include:_spf.google.com ~all"
  }'
```

**NS запись:**
```bash
curl -X POST http://localhost:8080/api/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{
    "name": "@",
    "type": "NS",
    "value": "ns2.test.local"
  }'
```

**Ответ (200 OK):**
```json
{
  "success": true,
  "message": "Запись добавлена",
  "data": null
}
```

**Ответ (200 OK — запись добавлена, PTR не создана):**
```json
{
  "success": true,
  "message": "Запись добавлена (PTR не создана: обратная зона не найдена)",
  "data": null
}
```

**Ответ (400 Bad Request):**
```json
{
  "success": false,
  "message": "Неверный IPv4 адрес",
  "data": null
}
```

---

## 9. Удаление записи из зоны

**Метод:** `DELETE`  
**URL:** `/api/zone/:name/record/:record/:type`

**Описание:** Удаляет запись по имени и типу. Для A/AAAA записей автоматически удаляется соответствующая PTR запись.

**Параметры URL:**

| Параметр | Описание |
|----------|----------|
| `:name` | Имя зоны (например, `test.local`) |
| `:record` | Имя записи (`www`, `@`, `mail`) |
| `:type` | Тип записи (`A`, `AAAA`, `CNAME` и т.д.) |

**Пример запроса:**
```bash
curl -X DELETE http://localhost:8080/api/zone/test.local/record/www/A
```

**Ответ (200 OK):**
```json
{
  "success": true,
  "message": "Запись удалена",
  "data": null
}
```

**Ответ (404 Not Found):**
```json
{
  "success": false,
  "message": "Запись не найдена",
  "data": null
}
```

---

## 🧪 Полный тестовый сценарий

```bash
#!/bin/bash

API_URL="http://localhost:8080/api"

echo "=== 1. Проверка статуса ==="
curl -s $API_URL/status | jq .

echo "=== 2. Проверка конфигурации ==="
curl -s $API_URL/config | jq .

echo "=== 3. Список зон ==="
curl -s $API_URL/zones | jq .

echo "=== 4. Создание зоны ==="
curl -s -X POST $API_URL/zone \
  -H "Content-Type: application/json" \
  -d '{"name": "test.local", "email": "admin.test.local", "ns_ip": "10.69.13.3"}' | jq .

echo "=== 5. Добавление A записей ==="
curl -s -X POST $API_URL/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{"name": "www", "type": "A", "value": "10.69.13.100"}' | jq .

curl -s -X POST $API_URL/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{"name": "blog", "type": "A", "value": "10.69.13.101", "reverse_ptr": "blog.test.local"}' | jq .

echo "=== 6. Добавление CNAME ==="
curl -s -X POST $API_URL/zone/test.local/record \
  -H "Content-Type: application/json" \
  -d '{"name": "shop", "type": "CNAME", "value": "www.test.local"}' | jq .

echo "=== 7. Просмотр зоны ==="
curl -s $API_URL/zone/test.local | jq .

echo "=== 8. Список зон (обновлённый) ==="
curl -s $API_URL/zones | jq .

echo "=== 9. Удаление записи ==="
curl -s -X DELETE $API_URL/zone/test.local/record/shop/CNAME | jq .

echo "=== 10. Удаление зоны ==="
curl -s -X DELETE $API_URL/zone/test.local | jq .

echo "=== 11. Перезагрузка BIND ==="
curl -s -X POST $API_URL/reload | jq .
```

---

## 📊 Коды ответов

| Код | Описание |
|-----|----------|
| `200` | Успешное выполнение |
| `400` | Ошибка валидации (неверный JSON, параметры, IP) |
| `404` | Зона или запись не найдена |
| `405` | Метод не разрешён |
| `409` | Конфликт (зона уже существует) |
| `500` | Внутренняя ошибка сервера (права, синтаксис, rndc) |

---

## 🔧 Переменные окружения

| Переменная | Описание | Значение по умолчанию |
|------------|----------|---------------------|
| `BIND_ZONE_DIR` | Директория для файлов зон | `/var/named/` |
| `BIND_ZONE_CONF` | Дополнительный файл конфигурации зон | `/etc/named.zones.conf` |
| `BIND_NAMED_CONF` | Основной файл конфигурации BIND | `/etc/named.conf` |
| `API_PORT` | Порт для запуска API | `:8080` |
| `GIN_MODE` | Режим Gin (`debug`/`release`) | `release` |

**Пример запуска с кастомными настройками:**
```bash
sudo BIND_ZONE_DIR=/custom/zones API_PORT=:9090 ./bind-api
```

---

## ⚠️ Требования и ограничения

1. **Права доступа:** Сервис должен запускаться от `root` для записи в `/etc` и `/var/named`
2. **rndc:** Должен быть настроен и работать (`rndc status`)
3. **SELinux:** Контексты файлов должны быть корректными (`restorecon`)
4. **Синтаксис:** Все изменения проверяются через `named-checkconf` и `named-checkzone`
5. **Serial:** Автоматически увеличивается при изменении зоны
6. **PTR записи:** Создаются/удаляются автоматически для A/AAAA при указании `reverse_ptr`

---