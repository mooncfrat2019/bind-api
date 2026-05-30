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
    bc \
    procps-ng \
    net-tools \
    lsof \
    && dnf clean all

# Настройка BIND
RUN mkdir -p /var/named /var/named/data /var/named/slaves /var/named/dynamic /var/run/named && \
    chown -R named:named /var/named /var/run/named && \
    chmod 755 /var/named

# Генерация ключа rndc (без -r, так как он deprecated)
RUN rndc-confgen -a -c /etc/rndc.key && \
    chown root:named /etc/rndc.key && \
    chmod 640 /etc/rndc.key

# Конфиг named.conf с добавлением rndc ключа
RUN printf '%s\n' \
    'options {' \
    '    listen-on port 53 { any; };' \
    '    listen-on-v6 port 53 { none; };' \
    '    directory "/var/named";' \
    '    allow-query { any; };' \
    '    recursion yes;' \
    '    allow-transfer { any; };' \
    '    allow-recursion { any; };' \
    '    pid-file "/var/run/named/named.pid";' \
    '    statistics-file "/var/named/named.stats";' \
    '};' \
    '' \
    'controls {' \
    '    inet 127.0.0.1 port 953 allow { 127.0.0.1; } keys { "rndc-key"; };' \
    '};' \
    '' \
    'zone "." IN {' \
    '    type hint;' \
    '    file "named.ca";' \
    '};' \
    '' \
    'include "/etc/named.zones.conf";' \
    'include "/etc/rndc.key";' > /etc/named.conf

# Создание файла корневых хинтов
RUN dig +nocmd +nocomments +noquestion +nostats NS . > /var/named/named.ca 2>/dev/null || \
    echo ". 518400 IN NS a.root-servers.net." > /var/named/named.ca

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

# Копирование скриптов и entrypoint
COPY test/load/scripts/ /app/scripts/
COPY test/load/data/ /app/data/
COPY test/load/entrypoint.sh /app/entrypoint.sh

RUN chmod +x /app/scripts/*.sh && \
    chmod +x /app/entrypoint.sh

ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["tail", "-f", "/dev/null"]