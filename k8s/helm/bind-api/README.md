# BIND-API Helm Chart

Helm chart для развёртывания BIND 9 DNS сервера с REST API управлением в Kubernetes.

## Архитектура

Каждый под содержит два контейнера:
1. **BIND 9** - DNS сервер (порт 53, 953)
2. **BIND-API** - REST API для управления (порт 8080)

Оба контейнера разделяют:
- `/var/named` - файлы зон
- `/etc/named.conf` - конфигурация BIND

## Требования

- Kubernetes 1.25+
- Helm 3.10+
- StorageClass для persistent volumes
- (опционально) Prometheus Operator для ServiceMonitor
- (опционально) Ingress Controller

## Быстрая установка

### 1. Мастер сервер
#### Установка мастера с PostgreSQL
```bash
helm install bind-api-master ./bind-api
--namespace bind-dns
--create-namespace
-f values.master.yaml
--set secrets.bootstrapKey="your-secure-bootstrap-key-min-32-chars"
--set secrets.syncToken="your-secure-sync-token"
--set secrets.masterApiToken="your-secure-master-api-token"
--set secrets.postgresPassword="your-secure-postgres-password"
--set secrets.rndcKeySecret="cm5kYy1zZWNyZXQta2V5LWJhc2U2NAo="
```
### 2. Реплики (опционально)
```bash
helm install bind-api-replica ./bind-api
--namespace bind-dns
-f values.replica.yaml
--set master.url="https://bind-api.example.com"
--set secrets.syncToken="your-secure-sync-token"
--set secrets.masterApiToken="your-secure-master-api-token"
```
## Проверка установки
### Проверка подов
kubectl get pods -n bind-dns
### Проверка что оба контейнера работают
kubectl describe pod -n bind-dns bind-api-master-0
### Логи BIND
kubectl logs -n bind-dns bind-api-master-0 -c bind9
### Логи API
kubectl logs -n bind-dns bind-api-master-0 -c bind-api

# Конфигурация

### Основные параметры

| Параметр | Описание | По умолчанию |
|----------|----------|--------------|
| `role` | Роль сервера (master/replica) | `master` |
| `replicaCount.master` | Количество мастеров | `1` |
| `replicaCount.replica` | Количество реплик | `2` |
| `image.repository` | Repository образа API | `bind-api` |
| `bind.image.repository` | Repository образа BIND9 | `ubuntu/bind9` |
| `postgresql.enabled` | Включить PostgreSQL | `true` |

### Секреты (ОБЯЗАТЕЛЬНО изменить!)

yaml secrets: postgresPassword: "CHANGE_ME" bootstrapKey: "CHANGE_ME_MIN_32_CHARS" syncToken: "CHANGE_ME" masterApiToken: "CHANGE_ME" rndcKeySecret: "CHANGE_ME_BASE64"

### Генерация RNDC ключа

Сгенерировать ключ
```
rndc-confgen -a -c /etc/rndc.key
```
Получить base64 значение
```
cat /etc/rndc.key | grep secret | awk '{print $2}' | tr -d '"' | base64
```

## Примеры использования

```bash
API_KEY="your-api-key"
curl -X POST "[https://bind-api.example.com/api/write/zone](https://bind-api.example.com/api/write/zone)"
-H "Content-Type: application/json"
-H "X-API-Key: $API_KEY"
-d '{ "name": "example.com", "email": "admin@example.com", "nsIP": "192.168.1.10" }'
``` 
### Проверить DNS

Через внутренний сервис
dig @bind-api-dns-internal.bind-dns.svc.cluster.local example.com
Через внешний IP
dig @<EXTERNAL_IP> example.com

## Мониторинг

### Prometheus метрики

Метрики доступны на `/metrics`:

- `bind_api_operations_total` - количество операций
- `bind_api_operation_duration_seconds` - время выполнения
- `bind_api_queue_size` - размер очереди
- `bind_api_zones_total` - количество зон
- `bind_api_records_total` - количество записей

## Backup

### PostgreSQL

```bash
kubectl exec -n bind-dns bind-api-master-postgresql-0 --
pg_dump -U bindapi bind_api > backup.sql
```

### Зоны

```bash
kubectl exec -n bind-dns bind-api-master-0 -c bind-api --
tar czf /tmp/zones-backup.tar.gz /var/named/
kubectl cp bind-dns/bind-api-master-0:/tmp/zones-backup.tar.gz ./zones-backup.tar.gz
```

## Troubleshooting

### Проверка статуса BIND
```bash
kubectl exec -n bind-dns bind-api-master-0 -c bind9 -- rndc status
```
### Перезагрузка BIND
```bash
kubectl exec -n bind-dns bind-api-master-0 -c bind9 -- rndc reload
```
### Проверка конфигурации
```bash
kubectl exec -n bind-dns bind-api-master-0 -c bind9 -- named-checkconf
```

### Логи
BIND логи
kubectl logs -n bind-dns bind-api-master-0 -c bind9 -f
API логи
kubectl logs -n bind-dns bind-api-master-0 -c bind-api -f

## Production Checklist

- [ ] Все секреты заменены на безопасные значения
- [ ] Bootstrap ключ отозван после создания постоянного API ключа
- [ ] HTTPS настроен через Ingress + cert-manager
- [ ] Backup PostgreSQL настроен
- [ ] Мониторинг и алерты настроены
- [ ] NetworkPolicies применены
- [ ] ResourceQuotas установлены
- [ ] PodDisruptionBudget создан