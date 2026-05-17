//go:build integration
// +build integration

package internal

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntegrationFullFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Настройка окружения
	os.Setenv("APP_ROLE", "master")
	os.Setenv("BIND_ZONE_DIR", "/tmp/bind-test/zones")
	os.Setenv("BIND_NAMED_CONF", "/tmp/bind-test/named.conf")

	// Создаём тестовые директории
	err := os.MkdirAll("/tmp/bind-test/zones", 0755)
	require.NoError(t, err)
	defer os.RemoveAll("/tmp/bind-test")

	// Инициализация
	InitConfig()
	AppRole = "master"

	// Создаём тестовый конфиг
	namedConf := `options {
        directory "/var/named";
    };`
	err = os.WriteFile("/tmp/bind-test/named.conf", []byte(namedConf), 0644)
	require.NoError(t, err)

	// Здесь можно добавить тесты полного цикла
	// Создание зоны -> добавление записи -> удаление записи -> удаление зоны
}
