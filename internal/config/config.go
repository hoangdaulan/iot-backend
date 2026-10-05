// Package config reads the server configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPPort           string
	DatabaseURL        string
	JWTSecret          string
	JWTTTL             time.Duration
	CORSAllowedOrigins []string
	RunMigrations      bool
	RunSeeds           bool
	MQTT               MQTT
}

type MQTT struct {
	Enabled        bool
	Broker         string
	Port           int
	Username       string
	Password       string
	ClientID       string
	QoS            byte
	SensorTopic    string
	ControlTopic   string
	ResponseTopic  string
	CommandTimeout time.Duration
}

// Load reads the configuration. JWT_SECRET is required; everything else has a development
// default.
func Load() (Config, error) {
	var errs []error
	cfg := Config{
		HTTPPort:           env("HTTP_PORT", "8080"),
		DatabaseURL:        env("DATABASE_URL", "postgres://myiot:myiot@localhost:5432/myiot?sslmode=disable"),
		JWTSecret:          os.Getenv("JWT_SECRET"),
		JWTTTL:             duration("JWT_TTL", 24*time.Hour, &errs),
		CORSAllowedOrigins: list("CORS_ALLOWED_ORIGINS", "*"),
		RunMigrations:      boolean("RUN_MIGRATIONS", true, &errs),
		RunSeeds:           boolean("RUN_SEEDS", false, &errs),
		MQTT: MQTT{
			Enabled:        boolean("MQTT_ENABLED", true, &errs),
			Broker:         env("MQTT_BROKER", "localhost"),
			Port:           integer("MQTT_PORT", 1883, &errs),
			Username:       os.Getenv("MQTT_USERNAME"),
			Password:       os.Getenv("MQTT_PASSWORD"),
			ClientID:       env("MQTT_CLIENT_ID", "myiot-backend"),
			QoS:            byte(integer("MQTT_QOS", 1, &errs)),
			SensorTopic:    env("MQTT_TOPIC_SENSOR_DATA", "myiot/esp32/sensor-data"),
			ControlTopic:   env("MQTT_TOPIC_DEVICE_CONTROL", "myiot/esp32/device-control"),
			ResponseTopic:  env("MQTT_TOPIC_DEVICE_RESPONSE", "myiot/esp32/device-response"),
			CommandTimeout: duration("MQTT_COMMAND_TIMEOUT", 5*time.Second, &errs),
		},
	}
	if cfg.JWTSecret == "" {
		errs = append(errs, errors.New("JWT_SECRET is required"))
	}
	if cfg.MQTT.QoS > 2 {
		errs = append(errs, errors.New("MQTT_QOS must be 0, 1 or 2"))
	}
	return cfg, errors.Join(errs...)
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func list(key, fallback string) []string {
	var out []string
	for _, part := range strings.Split(env(key, fallback), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func integer(key string, fallback int, errs *[]error) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
		return fallback
	}
	return v
}

func boolean(key string, fallback bool, errs *[]error) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
		return fallback
	}
	return v
}

func duration(key string, fallback time.Duration, errs *[]error) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
		return fallback
	}
	return v
}
