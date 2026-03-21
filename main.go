package main

import (
	"fmt"

	"log"

	"os"
	"os/exec"

	"strconv"
	"strings"

	app "bind-api/internal"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func loggerMiddleware() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return fmt.Sprintf("[BIND-API] %s | %3d | %13v | %15s | %-7s %s\n",
			param.TimeStamp.Format("2006/01/02 - 15:04:05"),
			param.StatusCode,
			param.Latency,
			param.ClientIP,
			param.Method,
			param.Path,
		)
	})
}

func main() {
	// Загрузка .env
	if err := godotenv.Load(); err != nil {
		log.Println("WARNING: .env файл не найден")
	}

	app.InitConfig()

	// Определяем роль
	app.AppRole = os.Getenv("APP_ROLE")
	if app.AppRole == "" {
		app.AppRole = "master"
	}
	log.Printf("=== РОЛЬ СЕРВЕРА: %s ===", strings.ToUpper(app.AppRole))

	// Инициализация БД (только MASTER)
	if err := app.InitDatabase(); err != nil {
		log.Fatalf("Ошибка инициализации БД: %v", err)
	}

	// Инициализация обработчика синхронизации (только MASTER)
	if app.AppRole == "master" {
		app.SH = app.NewSH(app.Db)
		log.Println("✓ Синхронизация MASTER инициализирована")

		app.InitJobQueue()
		log.Println("✓ Очередь заданий инициализирована")
	}

	// Инициализация клиента синхронизации (только REPLICA)
	if app.AppRole == "replica" {
		masterURL := os.Getenv("MASTER_URL")
		apiToken := os.Getenv("MASTER_API_TOKEN")
		syncInterval := 30
		if val := os.Getenv("SYNC_INTERVAL"); val != "" {
			syncInterval, _ = strconv.Atoi(val)
		}

		if masterURL == "" {
			log.Fatal("ERROR: MASTER_URL не указан для REPLICA")
		}

		app.RS = app.NewReplicaSync(masterURL, apiToken, syncInterval, true)
		app.RS.Start()
		log.Println("✓ Синхронизация REPLICA запущена")
	}

	// Проверка BIND
	if _, err := exec.LookPath("rndc"); err != nil {
		log.Fatal("Утилита rndc не найдена в PATH")
	}

	if _, err := os.Stat(app.ZoneDir); os.IsNotExist(err) {
		log.Fatalf("Директория зон не существует: %s", app.ZoneDir)
	}

	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(loggerMiddleware())
	r.Use(gin.Recovery())

	api := r.Group("/api")
	{
		api.GET("/status", app.HandleStatus)

		// Endpoints для синхронизации (ТОЛЬКО MASTER)
		if app.AppRole == "master" {
			sync0 := api.Group("/sync", app.SH.SyncAuthMiddleware())
			{
				sync0.GET("/state", app.SH.GetSyncState)
				sync0.GET("/state/:fileType/:fileName", app.SH.GetSyncFile)
				sync0.GET("/zones", app.SH.GetSyncZones)
				sync0.GET("/zone/:zoneName", app.SH.GetSyncZone)
				sync0.GET("/file", app.SH.GetSyncFileQuery)

				// Работа с версиями
				sync0.GET("/versions/:fileType", app.SH.GetVersions)
				sync0.GET("/version/:id", app.SH.GetVersion)
				sync0.POST("/version/:id/rollback", app.SH.RollbackVersion)
				sync0.DELETE("/version/:id", app.SH.DeleteVersion)
			}

			// Обычные API endpoints (ТОЛЬКО MASTER)
			api.GET("/config", app.HandleConfig)
			api.GET("/audit", app.HandleAuditLog)
			api.GET("/audit/stats", app.HandleAuditStats)
			api.POST("/reload", app.HandleReload)
			api.GET("/zones", app.HandleListZones)
			api.POST("/zone", app.HandleCreateZone)

			zones := api.Group("/zone/:name")
			{
				zones.GET("", app.HandleGetZone)
				zones.DELETE("", app.HandleDeleteZone)
				zones.POST("/record", app.HandleAddRecord)
				zones.DELETE("/record/:record/:type", app.HandleDeleteRecord)
			}
		} else {
			// REPLICA - только статус и информация о синхронизации
			api.GET("/sync/status", app.HandleReplicaStatus)
			api.GET("/sync/last-update", app.HandleReplicaLastUpdate)
		}
	}

	port := os.Getenv("API_PORT")
	if port == "" {
		port = ":8080"
	}

	log.Printf("BIND Manager API запущен на порту %s", port)
	log.Printf("Режим: %s", app.AppRole)

	if app.AppRole == "master" {
		log.Printf("База данных: %s@%s:%s/%s", app.DbUser, app.DbHost, app.DbPort, app.DbName)
	} else {
		log.Printf("MASTER URL: %s", os.Getenv("MASTER_URL"))
	}

	if err := r.Run(port); err != nil {
		log.Fatal(err)
	}
}
