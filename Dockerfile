# =============================================================================
# Dockerfile для BIND-API
# =============================================================================

# --- Этап 1: Сборка Go приложения ---
FROM golang:1.26-alpine AS builder

WORKDIR /build

# Установка зависимостей для сборки
RUN apk add --no-cache git ca-certificates

# Копирование go.mod и go.sum для кэширования
COPY go.mod go.sum ./
RUN go mod download

# Копирование исходного кода
COPY . .

# Сборка приложения.
# TARGETARCH заполняется автоматически при docker buildx build --platform ...
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-w -s" -o /build/bind-api .

# --- Этап 2: Финальный образ ---
FROM ubuntu:22.04

LABEL maintainer="devops@example.com"
LABEL description="BIND 9 DNS Server with REST API management"
LABEL version="1.0.0"

# Переменные окружения
ENV BIND_API_DB_HOST=localhost
ENV BIND_API_DB_PORT=5432
ENV BIND_API_DB_USER=bindapi
ENV BIND_API_DB_NAME=bind_api
ENV BIND_ZONE_DIR=/var/named
ENV BIND_NAMED_CONF=/config/named.conf
ENV BIND_ZONE_CONF=/config/named.zones.conf
ENV API_PORT=:8080
ENV APP_ROLE=master
ENV LOG_LEVEL=INFO

# Установка BIND9 и утилит (в контейнере bind-api нужны named-checkconf/named-checkzone/rndc/dig)
RUN apt-get update && apt-get install -y \
        bind9 \
        bind9utils \
        dnsutils \
        ca-certificates \
        curl \
        && apt-get clean \
        && rm -rf /var/lib/apt/lists/*

# Создание пользователя и групп
RUN groupadd -r named && useradd -r -g named named

# Создание директорий
RUN mkdir -p /var/named /etc/bind /app \
        && chown -R root:named /var/named \
        && chmod 775 /var/named

# Копирование бинарного файла из builder
COPY --from=builder /build/bind-api /app/bind-api

RUN chown root:named /app/bind-api \
        && chmod 750 /app/bind-api

# Симлинки для утилит BIND.
# named-checkconf / named-checkzone без -c читают /etc/named.conf.
# rndc без -c ищет /etc/rndc.conf, затем /etc/bind/rndc.conf, затем *.key.
# Рабочие конфиги лежат в /config (writable каталог, монтируется в поде),
# поэтому направляем дефолтные пути туда: тогда ЛЮБОЙ вызов rndc (в том числе
# без -c) подписывается правильным ключом и не даёт "bad auth".
RUN ln -sf /config/named.conf /etc/named.conf \
        && ln -sf /config/named.zones.conf /etc/named.zones.conf \
        && ln -sf /config/rndc.conf /etc/rndc.conf \
        && ln -sf /config/rndc.conf /etc/bind/rndc.conf \
        && ln -sf /config/rndc.key /etc/rndc.key \
        && ln -sf /config/rndc.key /etc/bind/rndc.key

# Экспозиция портов
# 53 - DNS (TCP/UDP)
# 8080 - API
# 953 - RNDC
EXPOSE 53/tcp 53/udp 8080/tcp 953/tcp

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 \
        CMD curl -f http://localhost:8080/health || exit 1

# Точка входа
WORKDIR /app
ENTRYPOINT ["/app/bind-api"]