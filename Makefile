.PHONY: test test-all test-coverage test-race shell clean test-load test-load-setup test-load-run test-load-cleanup

# Запуск тестов internal
test:
	docker-compose -f docker-compose.test.yml up --build test

# Запуск всех тестов
test-all:
	docker-compose -f docker-compose.test.yml up --build test-all

# Запуск тестов с покрытием
test-coverage:
	docker-compose -f docker-compose.test.yml up --build test-coverage

# Запуск тестов с детектором гонок
test-race:
	docker-compose -f docker-compose.test.yml up --build test-race

# Интерактивная оболочка
shell:
	docker-compose -f docker-compose.test.yml run --rm shell

# Очистка
clean:
	docker-compose -f docker-compose.test.yml down -v
	docker system prune -f

# Запуск конкретного теста
test-specific:
	docker-compose -f docker-compose.test.yml run --rm test go test ./internal/... -v -run $(TEST)

test-load-setup:
	@echo "=== Настройка тестового окружения ==="
	cd test/load && docker-compose up -d
	@echo "Ожидание запуска контейнеров..."
	sleep 10
	cd test/load && docker-compose exec -T load-client /app/scripts/setup.sh

test-load-run:
	@echo "=== Запуск нагрузочных тестов ==="
	cd test/load && docker-compose exec -T load-client /app/scripts/run-test.sh

test-load-cleanup:
	@echo "=== Очистка тестового окружения ==="
	cd test/load && docker-compose down -v 2>/dev/null || true
	rm -rf test/load/results/* test/load/reports/* 2>/dev/null || true

test-load: test-load-setup test-load-run test-load-cleanup
	@echo "=== Все тесты завершены ==="