.PHONY: test test-add test-validation test-batch test-all setup clean

# Переменные
ZONE_NAME ?= loadtest-$(shell date +%s).local
DURATION ?= 100
RPS ?= 20
BATCH_SIZE ?= 100
CLEANUP=false

# Запуск теста по умолчанию (добавление записей) - RECORDS_COUNT вычисляется автоматически
test:
	@echo "=== Running add records test ==="
	@docker-compose exec -T app bash -c " \
		TEST_TYPE=add_records \
		DURATION=$(DURATION) \
		RPS=$(RPS) \
		CLEANUP=$(CLEANUP) \
		./scripts/run-test.sh"

# Тест добавления записей
test-add:
	@echo "=== Running add records test ==="
	@docker-compose exec -T app bash -c " \
		TEST_TYPE=add_records \
		DURATION=$(DURATION) \
		RPS=$(RPS) \
		./scripts/run-test.sh"

# Тест с кастомными параметрами
test-custom:
	@echo "=== Running custom test ==="
	@docker-compose exec -T app bash -c " \
		TEST_TYPE=add_records \
		DURATION=$(DURATION) \
		RPS=$(RPS) \
		./scripts/run-test.sh"

# Тест валидации
test-validation:
	@echo "=== Running validation test ==="
	@docker-compose exec -T app bash -c " \
		TEST_TYPE=validation \
		./scripts/run-test.sh"

# Тест batch режима
test-batch:
	@echo "=== Running batch mode test ==="
	@docker-compose exec -T app bash -c " \
		TEST_TYPE=batch \
		DURATION=45 \
		RPS=30 \
		BATCH_SIZE=$(BATCH_SIZE) \
		./scripts/run-test.sh"

# Запуск всех тестов
test-all:
	@echo "=== Running all tests ==="
	@docker-compose exec -T app bash -c " \
		TEST_TYPE=all \
		DURATION=30 \
		RPS=10 \
		./scripts/run-test.sh"

# Настройка окружения
setup:
	@echo "=== Setting up test environment ==="
	@docker-compose exec -T app ./scripts/setup.sh

# Очистка результатов
clean:
	@echo "=== Cleaning up results ==="
	@docker-compose exec -T app bash -c "rm -rf /app/results/*"
	@echo "Done"

# Полная очистка
clean-all: clean
	@docker-compose down -v
	@docker-compose up -d
	@echo "Waiting for services to start..."
	@sleep 10
	@$(MAKE) setup

# Помощь
help:
	@echo "Available commands:"
	@echo "  make test              - Run add records test (default)"
	@echo "  make test-add          - Run add records test"
	@echo "  make test-custom       - Run custom test (use DURATION and RPS)"
	@echo "  make test-validation   - Run validation test"
	@echo "  make test-batch        - Run batch mode test"
	@echo "  make test-all          - Run all tests"
	@echo "  make setup             - Setup test environment"
	@echo "  make clean             - Clean up results"
	@echo "  make clean-all         - Full cleanup (down + up + setup)"
	@echo ""
	@echo "Examples:"
	@echo "  make test-add DURATION=60 RPS=20"
	@echo "  make test-custom DURATION=120 RPS=50"