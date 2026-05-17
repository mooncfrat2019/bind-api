.PHONY: test test-all test-coverage test-race shell clean

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