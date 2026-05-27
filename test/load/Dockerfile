FROM registry.red-soft.ru/redos7c/ubi:latest

# Установка пакетов
RUN dnf install -y \
    bind \
    bind-utils \
    golang \
    postgresql \
    git \
    make \
    curl \
    wget \
    jq \
    && dnf clean all

# Настройка BIND
RUN mkdir -p /var/named /var/named/data /var/named/slaves /var/named/dynamic /var/run/named && \
    chown -R named:named /var/named /var/run/named && \
    chmod 755 /var/named

# Конфиг named.conf
RUN cat > /etc/named.conf << 'EOF'
options {
    listen-on port 53 { any; };
    listen-on-v6 port 53 { none; };
    directory "/var/named";
    allow-query { any; };
    recursion no;
    allow-transfer { any; };
    pid-file "/var/run/named/named.pid";
};

zone "." IN {
    type hint;
    file "named.ca";
};

include "/etc/named.zones.conf";
EOF

RUN touch /etc/named.zones.conf && chown named:named /etc/named.zones.conf

# Установка vegeta и hey
RUN mkdir -p /app
WORKDIR /app
RUN wget -q https://github.com/tsenart/vegeta/releases/download/v12.13.0/vegeta_12.13.0_linux_amd64.tar.gz && \
    tar -xzf vegeta_12.13.0_linux_amd64.tar.gz && \
    mv vegeta /usr/local/bin/ && \
    rm vegeta_12.13.0_linux_amd64.tar.gz
RUN wget -q https://storage.googleapis.com/hey-releases/hey_linux_amd64 -O /usr/local/bin/hey && \
    chmod +x /usr/local/bin/hey

# Копирование скриптов
COPY test/load/scripts/ /app/scripts/
COPY test/load/data/ /app/data/

RUN chmod +x /app/scripts/*.sh

CMD ["tail", "-f", "/dev/null"]