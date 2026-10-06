// @title MyIoT API
// @version 1.0
// @description REST API for the MyIoT sensors and devices.
// @BasePath /api
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Type "Bearer" followed by the access token.

// Command server runs the MyIoT REST API and MQTT bridge.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"iot-backend/internal/config"
	"iot-backend/internal/database"
	"iot-backend/internal/mqtt"
	"iot-backend/internal/repository/postgres"
	"iot-backend/internal/router"
	"iot-backend/internal/service"
	"iot-backend/migrations"
	"iot-backend/seeds"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Connect(ctx, cfg.DatabaseURL, time.Minute, logger)
	if err != nil {
		return err
	}
	defer pool.Close()
	if cfg.RunMigrations {
		if err := database.Migrate(ctx, pool, migrations.Files, logger); err != nil {
			return err
		}
	}
	if cfg.RunSeeds {
		if err := database.Seed(ctx, pool, seeds.Files, logger); err != nil {
			return err
		}
	}

	users := postgres.NewUserRepository(pool)
	sensorRepo := postgres.NewSensorRepository(pool)
	deviceRepo := postgres.NewDeviceRepository(pool)

	tokens := service.NewTokenService(cfg.JWTSecret, cfg.JWTTTL)
	auth := service.NewAuthService(users, tokens).WithUploadDir(cfg.UploadDir)
	sensors := service.NewSensorService(sensorRepo, deviceRepo)

	// The MQTT client needs the device service for responses and vice versa, so the client is
	// created first and connected once both exist.
	var devices *service.DeviceService
	var publisher service.Publisher = mqtt.Disabled{}
	var mqttClient *mqtt.Client
	if cfg.MQTT.Enabled {
		mqttClient = mqtt.New(cfg.MQTT, mqtt.Handlers{
			SensorData:     func(p mqtt.SensorPayload) { ingest(sensors, p, logger) },
			DeviceResponse: func(r service.DeviceResponse) { devices.HandleResponse(r) },
		}, logger)
		publisher = mqttClient
	} else {
		logger.Warn("MQTT disabled: device commands will fail and no sensor data is ingested")
	}
	devices = service.NewDeviceService(deviceRepo, publisher, cfg.MQTT.CommandTimeout, logger)
	if mqttClient != nil {
		mqttClient.Connect(5 * time.Second)
		defer mqttClient.Close()
	}

	gin.SetMode(gin.ReleaseMode)
	srv := &http.Server{
		Addr: ":" + cfg.HTTPPort,
		Handler: router.New(router.Deps{
			Auth: auth, Tokens: tokens, Sensors: sensors, Devices: devices,
			CORSAllowedOrigins: cfg.CORSAllowedOrigins, UploadDir: cfg.UploadDir, Logger: logger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		// Must outlast a device command, which waits up to MQTT_COMMAND_TIMEOUT.
		WriteTimeout: cfg.MQTT.CommandTimeout + 30*time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		logger.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.MQTT.CommandTimeout+5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// ingest stores one MQTT sensor message as SensorData rows.
func ingest(sensors *service.SensorService, p mqtt.SensorPayload, logger *slog.Logger) {
	at := time.Now().UTC()
	if p.Timestamp != nil {
		at = *p.Timestamp
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sensors.Ingest(ctx, p.Values, at); err != nil {
		logger.Error("store sensor data", "error", err)
	}
}
