.PHONY: up down setup test clean

up:
	docker-compose up -d

down:
	docker-compose down -v

setup:
	docker-compose exec -T app /app/scripts/setup.sh

test:
	docker-compose exec -T app /app/scripts/run-test.sh

clean: down
	rm -rf results/*

all: up setup test
	@echo "=== Все тесты завершены ==="