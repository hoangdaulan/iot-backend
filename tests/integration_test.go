package tests

// Integration tests against real services. They are skipped unless configured:
//
//	TEST_DATABASE_URL  PostgreSQL URL. The public schema is DROPPED and recreated: use a
//	                   throwaway database.
//	TEST_MQTT_BROKER   host:port of an MQTT broker (also needs TEST_DATABASE_URL).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"iot-backend/internal/config"
	"iot-backend/internal/database"
	"iot-backend/internal/model"
	"iot-backend/internal/mqtt"
	"iot-backend/internal/repository/postgres"
	"iot-backend/internal/router"
	"iot-backend/internal/service"
	"iot-backend/internal/vntime"
	"iot-backend/migrations"
	"iot-backend/seeds"
)

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// freshDatabase recreates the schema, then migrates and seeds twice to prove both are idempotent.
func freshDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, url, 30*time.Second, quietLogger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := database.Migrate(ctx, pool, migrations.Files, quietLogger); err != nil {
			t.Fatal(err)
		}
		if err := database.Seed(ctx, pool, seeds.Files, quietLogger); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// postgresHarness serves the real router over the PostgreSQL repositories.
func postgresHarness(t *testing.T, pool *pgxpool.Pool, publisher service.Publisher, timeout time.Duration) (*harness, *service.DeviceService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tokens := service.NewTokenService("test-secret", time.Hour)
	deviceRepo := postgres.NewDeviceRepository(pool)
	sensors := service.NewSensorService(postgres.NewSensorRepository(pool), deviceRepo).
		WithSettings(postgres.NewSettingsRepository(pool), nil)
	devices := service.NewDeviceService(deviceRepo, publisher, timeout, quietLogger)
	return &harness{
		t: t, sensors: sensors,
		handler: router.New(router.Deps{
			Auth: service.NewAuthService(postgres.NewUserRepository(pool), tokens), Tokens: tokens,
			Sensors: sensors, Devices: devices, CORSAllowedOrigins: []string{"*"}, Logger: quietLogger,
		}),
	}, devices
}

func TestPostgresSchemaAndSeeds(t *testing.T) {
	pool := freshDatabase(t)

	if n := count(t, pool, `SELECT count(*) FROM devices WHERE id = 1 AND name = 'LED 1' AND type = 'LED' AND status = 'OFF'`); n != 1 {
		t.Errorf("device rows = %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM devices WHERE type = 'LED'`); n != 2 {
		t.Errorf("two LED devices expected, got %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM devices WHERE id = 3 AND name = 'LED WIND' AND type = 'Warning Led'`); n != 1 {
		t.Errorf("the warning LED (device 3) = %d rows", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM sensors WHERE type = 'wind_speed' AND unit = 'km/h'`); n != 1 {
		t.Errorf("wind speed sensor = %d rows", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM sensor_data d JOIN sensors s ON s.id = d.sensor_id WHERE s.type = 'wind_speed' AND d.value NOT BETWEEN 0 AND 500`); n != 0 {
		t.Errorf("%d seeded wind speeds outside 0-500", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM sensors WHERE type IN ('temperature','humidity','light','wind_speed')`); n != 4 {
		t.Errorf("sensor rows = %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM users`); n != 2 {
		t.Errorf("users = %d (seeds must be idempotent)", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM users WHERE password LIKE '$2a$%'`); n != 2 {
		t.Errorf("bcrypt-hashed users = %d", n)
	}
	// 7 days every 10 minutes, per sensor, inserted once.
	if n := count(t, pool, `SELECT count(*) FROM sensor_data`); n != 4*1009 {
		t.Errorf("seeded sensor_data = %d, want %d", n, 4*1009)
	}
	if n := count(t, pool, `SELECT count(*) FROM device_actions`); n != 40 {
		t.Errorf("seeded device_actions = %d", n)
	}
	for _, index := range []string{"sensor_data_sensor_id_idx", "sensor_data_timestamp_idx", "sensor_data_sensor_id_timestamp_idx"} {
		if n := count(t, pool, `SELECT count(*) FROM pg_indexes WHERE indexname = $1`, index); n != 1 {
			t.Errorf("missing index %s", index)
		}
	}
}

func TestPostgresAPI(t *testing.T) {
	pool := freshDatabase(t)
	esp := &fakeESP32{}
	h, devices := postgresHarness(t, pool, esp, 200*time.Millisecond)
	esp.devices = devices
	esp.answer(model.ResultSuccess, "")

	// Seeded pgcrypto bcrypt hashes verify with Go's bcrypt.
	token := h.login("admin")
	expect(t, h.do(http.MethodPost, "/api/auth/login", "",
		map[string]string{"username": "admin", "password": "nope"}), http.StatusUnauthorized, "Invalid email or password")

	t.Run("register conflict comes from the unique constraint", func(t *testing.T) {
		expect(t, h.do(http.MethodPost, "/api/auth/register", "",
			map[string]string{"username": "fresh", "email": "fresh@x.io", "password": "123456"}), http.StatusCreated, "")
		expect(t, h.do(http.MethodPost, "/api/auth/register", "",
			map[string]string{"username": "fresh", "email": "other@x.io", "password": "123456"}), http.StatusConflict, "")
	})

	t.Run("profile update and password change", func(t *testing.T) {
		res := h.do(http.MethodPatch, "/api/auth/profile", token, map[string]string{"figma": "https://figma.com/@x"})
		expect(t, res, http.StatusOK, "")
		if p := res.json(t); p["figma"] != "https://figma.com/@x" || p["github"] != "https://github.com/myiot-admin" {
			t.Errorf("profile = %v", p)
		}
		userToken := h.login("user01")
		expect(t, h.do(http.MethodPatch, "/api/auth/password", userToken,
			map[string]string{"oldPassword": "123456", "newPassword": "654321"}), http.StatusNoContent, "")
		expect(t, h.do(http.MethodPost, "/api/auth/login", "",
			map[string]string{"username": "user01", "password": "654321"}), http.StatusOK, "")
	})

	t.Run("latest returns the newest row per sensor", func(t *testing.T) {
		future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		h.ingest(future, map[model.SensorType]float64{"temperature": 31.5, "light": 999})
		res := h.do(http.MethodGet, "/api/sensor-data/latest", token, nil)
		expect(t, res, http.StatusOK, "")
		var latest struct {
			Data map[string]struct {
				Value     float64
				Timestamp time.Time
			}
			DeviceStatus string
		}
		_ = json.Unmarshal(res.Body, &latest)
		if latest.Data["temperature"].Value != 31.5 || !latest.Data["temperature"].Timestamp.Equal(future) ||
			latest.Data["light"].Value != 999 || latest.Data["humidity"].Timestamp.Equal(future) ||
			latest.DeviceStatus != "OFF" {
			t.Errorf("latest = %s", res.Body)
		}
	})

	t.Run("history filters and pages in SQL", func(t *testing.T) {
		var b historyBody
		_ = json.Unmarshal(h.do(http.MethodGet, "/api/sensor-data/history?type=temperature&size=100", token, nil).Body, &b)
		if b.TotalElements != 1010 || b.TotalPages != 11 || len(b.Data["temperature"]) != 100 || len(b.Data["humidity"]) != 0 {
			t.Errorf("type filter: total=%d pages=%d rows=%d", b.TotalElements, b.TotalPages, len(b.Data["temperature"]))
		}
		if b.Data["temperature"][0].Value != 31.5 {
			t.Errorf("newest first: %v", b.Data["temperature"][0])
		}
		from := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
		_ = json.Unmarshal(h.do(http.MethodGet, "/api/sensor-data/history?startTime="+from+"&size=5000", token, nil).Body, &b)
		if b.TotalElements < 4*12 || b.TotalElements > 4*14+2 {
			t.Errorf("last 2 hours: total = %d", b.TotalElements)
		}
		_ = json.Unmarshal(h.do(http.MethodGet, "/api/sensor-data/history?value=999", token, nil).Body, &b)
		if b.TotalElements != 1 || len(b.Data["light"]) != 1 {
			t.Errorf("value filter: %+v", b)
		}
	})

	t.Run("history buckets and searches in SQL", func(t *testing.T) {
		var b historyBody
		get := func(query string) {
			t.Helper()
			res := h.do(http.MethodGet, "/api/sensor-data/history"+query, token, nil)
			expect(t, res, http.StatusOK, "")
			b = historyBody{}
			_ = json.Unmarshal(res.Body, &b)
		}

		// The 31.5 row ingested above, alone in its 5-minute window.
		future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		window := future.Truncate(5 * time.Minute)
		get("?bucket=5m&type=temperature&startTime=" + future.Add(-time.Minute).Format(time.RFC3339))
		if b.TotalElements != 1 || b.Data["temperature"][0].Value != 31.5 {
			t.Errorf("bucket of the newest row: %+v", b)
		}
		var raw struct {
			Data map[string][]struct{ Timestamp time.Time }
		}
		res := h.do(http.MethodGet, "/api/sensor-data/history?bucket=5m&type=temperature&size=1", token, nil)
		_ = json.Unmarshal(res.Body, &raw)
		if got := raw.Data["temperature"][0].Timestamp; !got.Equal(window) {
			t.Errorf("window start = %v, want %v", got, window)
		}

		// A week of 10-minute readings grouped by the hour: one window per sensor and hour.
		want := count(t, pool, `SELECT count(*) FROM (SELECT 1 FROM sensor_data d JOIN sensors s ON s.id = d.sensor_id
			WHERE s.type = 'humidity' GROUP BY s.id, floor(extract(epoch FROM d.timestamp) / 3600)) g`)
		get("?bucket=1h&type=humidity&size=5000")
		if b.TotalElements != want {
			t.Errorf("hourly humidity windows = %d, want %d", b.TotalElements, want)
		}

		// Searches agree with plain SQL over the same rows.
		get("?filter=sensor&q=hum&size=1")
		if want := count(t, pool, `SELECT count(*) FROM sensor_data d JOIN sensors s ON s.id = d.sensor_id WHERE s.type = 'humidity'`); b.TotalElements != want {
			t.Errorf("sensor name search = %d, want %d", b.TotalElements, want)
		}
		get("?filter=temperature&q=31&size=1")
		if want := count(t, pool, `SELECT count(*) FROM sensor_data d JOIN sensors s ON s.id = d.sensor_id
			WHERE s.type = 'temperature' AND d.value >= 31 AND d.value < 32`); b.TotalElements != want || want == 0 {
			t.Errorf("value search = %d, want %d", b.TotalElements, want)
		}
		get("?filter=time&q=" + future.In(vntime.Loc).Format("2006/01/02") + "&size=1")
		day := time.Date(future.In(vntime.Loc).Year(), future.In(vntime.Loc).Month(), future.In(vntime.Loc).Day(), 0, 0, 0, 0, vntime.Loc)
		if want := count(t, pool, `SELECT count(*) FROM sensor_data WHERE timestamp >= $1 AND timestamp < $2`,
			day, day.Add(24*time.Hour)); b.TotalElements != want {
			t.Errorf("time search = %d, want %d", b.TotalElements, want)
		}
		get("?filter=all&q=31&size=1")
		if want := count(t, pool, `SELECT count(*) FROM sensor_data d JOIN sensors s ON s.id = d.sensor_id
			WHERE s.id = 31 OR s.name ILIKE '%31%' OR (d.value >= 31 AND d.value < 32)`); b.TotalElements != want {
			t.Errorf("union search = %d, want %d", b.TotalElements, want)
		}
	})

	t.Run("command persists the action and the device status", func(t *testing.T) {
		res := h.do(http.MethodPost, "/api/devices/1/command", token, `{"command":"ON"}`)
		expect(t, res, http.StatusOK, "Device turned on successfully")
		if n := count(t, pool, `SELECT count(*) FROM devices WHERE id = 1 AND status = 'ON'`); n != 1 {
			t.Error("device status not updated")
		}
		esp.stayQuiet()
		expect(t, h.do(http.MethodPost, "/api/devices/1/command", token, `{"command":"OFF"}`), http.StatusGatewayTimeout, "")
		if n := count(t, pool, `SELECT count(*) FROM device_actions WHERE result = 'PENDING'`); n != 0 {
			t.Errorf("%d actions left PENDING", n)
		}
		expect(t, h.do(http.MethodPost, "/api/devices/4/command", token, `{"command":"ON"}`), http.StatusNotFound, "Device not found")
	})

	t.Run("control history", func(t *testing.T) {
		var page struct {
			Content       []map[string]any
			TotalElements int64
			TotalPages    int
		}
		_ = json.Unmarshal(h.do(http.MethodGet, "/api/devices/control-history?size=5", token, nil).Body, &page)
		if page.TotalElements != 42 || page.TotalPages != 9 || len(page.Content) != 5 {
			t.Fatalf("page = %+v", page)
		}
		newest := page.Content[0]
		if newest["action"] != "TURN_OFF" || newest["result"] != "TIMEOUT" || newest["deviceName"] != "LED 1" {
			t.Errorf("newest = %v", newest)
		}
		if second := page.Content[1]; second["result"] != "SUCCESS" || second["status"] != "ON" {
			t.Errorf("second = %v", second)
		}
		from := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		_ = json.Unmarshal(h.do(http.MethodGet, "/api/devices/control-history?deviceId=1&startTime="+from, token, nil).Body, &page)
		if page.TotalElements != 2 {
			t.Errorf("recent actions = %d", page.TotalElements)
		}
	})

	t.Run("wind warnings per day agree with plain SQL", func(t *testing.T) {
		var report struct {
			Total int
			Days  []struct {
				Date  string
				Count int
			}
		}
		res := h.do(http.MethodGet, "/api/reports/wind-warnings", token, nil)
		expect(t, res, http.StatusOK, "")
		_ = json.Unmarshal(res.Body, &report)
		monthStart := time.Now().In(vntime.Loc)
		monthStart = time.Date(monthStart.Year(), monthStart.Month(), 1, 0, 0, 0, 0, vntime.Loc)
		want := count(t, pool, `SELECT count(*) FROM sensor_data d JOIN sensors s ON s.id = d.sensor_id
			WHERE s.type = 'wind_speed' AND d.value >= 300 AND d.timestamp >= $1 AND d.timestamp < $2`,
			monthStart, monthStart.AddDate(0, 1, 0))
		if int64(report.Total) != want {
			t.Errorf("total = %d, want %d", report.Total, want)
		}
		sum := 0
		for _, d := range report.Days {
			sum += d.Count
			day, _ := time.ParseInLocation("2006-01-02", d.Date, vntime.Loc)
			if n := count(t, pool, `SELECT count(*) FROM sensor_data d JOIN sensors s ON s.id = d.sensor_id
				WHERE s.type = 'wind_speed' AND d.value >= 300 AND d.timestamp >= $1 AND d.timestamp < $2`,
				day, day.AddDate(0, 0, 1)); int64(d.Count) != n {
				t.Errorf("%s: %d, want %d", d.Date, d.Count, n)
			}
		}
		if sum != report.Total {
			t.Errorf("days add up to %d, total %d", sum, report.Total)
		}
	})

	t.Run("the wind threshold is stored in the settings table", func(t *testing.T) {
		if got := h.do(http.MethodGet, "/api/settings/wind-threshold", token, nil).json(t)["threshold"]; got != float64(300) {
			t.Errorf("default threshold = %v", got)
		}
		expect(t, h.do(http.MethodPut, "/api/settings/wind-threshold", token, `{"threshold":275}`), http.StatusOK, "")
		if n := count(t, pool, `SELECT count(*) FROM settings WHERE key = 'wind_warning_threshold' AND value = '275'`); n != 1 {
			t.Errorf("settings rows = %d", n)
		}
		// restore, so later checks use the default
		expect(t, h.do(http.MethodPut, "/api/settings/wind-threshold", token, `{"threshold":300}`), http.StatusOK, "")
	})

	t.Run("warning LED changes are recorded by the system", func(t *testing.T) {
		before := count(t, pool, `SELECT count(*) FROM device_actions WHERE device_id = 3 AND user_id IS NULL`)
		now := time.Now().UTC()
		h.ingest(now, map[model.SensorType]float64{"wind_speed": 350})
		h.ingest(now, map[model.SensorType]float64{"wind_speed": 360}) // still on: no new row
		h.ingest(now, map[model.SensorType]float64{"wind_speed": 20})
		if n := count(t, pool, `SELECT count(*) FROM device_actions WHERE device_id = 3 AND user_id IS NULL`); n != before+2 {
			t.Errorf("system actions of LED WIND = %d, want %d", n, before+2)
		}
		if n := count(t, pool, `SELECT count(*) FROM device_actions WHERE device_id = 3 AND user_id IS NULL
			AND result = 'SUCCESS' AND completed_at IS NOT NULL AND message LIKE 'Wind speed%'`); n != before+2 {
			t.Errorf("completed system actions = %d", n)
		}
		if n := count(t, pool, `SELECT count(*) FROM devices WHERE id = 3 AND status = 'OFF'`); n != 1 {
			t.Errorf("LED WIND should be off again")
		}
	})

}

// TestMQTTEndToEnd runs the real MQTT client against a broker, with a simulated ESP32.
func TestMQTTEndToEnd(t *testing.T) {
	broker := os.Getenv("TEST_MQTT_BROKER")
	if broker == "" {
		t.Skip("TEST_MQTT_BROKER not set")
	}
	pool := freshDatabase(t)
	host, portRaw, err := net.SplitHostPort(broker)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portRaw)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	cfg := config.MQTT{
		Enabled: true, Broker: host, Port: port, ClientID: "myiot-test-backend-" + suffix, QoS: 1,
		SensorTopic:   "test/" + suffix + "/sensor-data",
		ControlTopic:  "test/" + suffix + "/device-control",
		ResponseTopic: "test/" + suffix + "/device-response",
		ConfigTopic:   "test/" + suffix + "/config",
	}

	deviceRepo := postgres.NewDeviceRepository(pool)
	sensors := service.NewSensorService(postgres.NewSensorRepository(pool), deviceRepo)
	var devices *service.DeviceService
	client := mqtt.New(cfg, mqtt.Handlers{
		SensorData: func(p mqtt.SensorPayload) {
			if _, err := sensors.Ingest(context.Background(), p.Values, time.Now().UTC()); err != nil {
				t.Errorf("ingest: %v", err)
			}
		},
		DeviceResponse: func(r service.DeviceResponse) { devices.HandleResponse(r) },
	}, quietLogger)
	devices = service.NewDeviceService(deviceRepo, client, 3*time.Second, quietLogger)
	sensors.WithSettings(postgres.NewSettingsRepository(pool), client)
	client.Connect(5 * time.Second)
	t.Cleanup(client.Close)

	// Simulated ESP32: switches the LED on every command and reports back.
	esp := paho.NewClient(paho.NewClientOptions().AddBroker(fmt.Sprintf("tcp://%s", broker)).
		SetClientID("myiot-test-esp32-" + suffix))
	if tok := esp.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("esp connect: %v", tok.Error())
	}
	t.Cleanup(func() { esp.Disconnect(100) })
	tok := esp.Subscribe(cfg.ControlTopic, 1, func(c paho.Client, m paho.Message) {
		var cmd struct {
			ActionID int64  `json:"actionId"`
			Command  string `json:"command"`
		}
		_ = json.Unmarshal(m.Payload(), &cmd)
		reply, _ := json.Marshal(map[string]any{"actionId": cmd.ActionID, "status": "SUCCESS", "state": cmd.Command})
		c.Publish(cfg.ResponseTopic, 1, false, reply)
	})
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("esp subscribe: %v", tok.Error())
	}
	waitFor(t, "backend subscriptions", client.Ready)

	// A new wind threshold is stored and sent retained, so an ESP32 that subscribes later gets it.
	if err := sensors.SetWindThreshold(context.Background(), 275); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM settings WHERE key = 'wind_warning_threshold' AND value = '275'`); n != 1 {
		t.Errorf("stored threshold rows = %d", n)
	}
	got := make(chan string, 1)
	late := paho.NewClient(paho.NewClientOptions().AddBroker(fmt.Sprintf("tcp://%s", broker)).
		SetClientID("myiot-test-late-esp32-" + suffix))
	if tok := late.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("late esp connect: %v", tok.Error())
	}
	t.Cleanup(func() { late.Disconnect(100) })
	late.Subscribe(cfg.ConfigTopic, 1, func(_ paho.Client, m paho.Message) { got <- string(m.Payload()) })
	select {
	case payload := <-got:
		if payload != `{"windThreshold":275}` {
			t.Errorf("retained config = %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Error("no retained config received")
	}

	before := count(t, pool, `SELECT count(*) FROM sensor_data`)
	lastID := count(t, pool, `SELECT max(id) FROM sensor_data`)
	esp.Publish(cfg.SensorTopic, 1, false, `not json`).Wait()
	esp.Publish(cfg.SensorTopic, 1, false, `{"humidity": 250}`).Wait()
	esp.Publish(cfg.SensorTopic, 1, false, `{"temperature": 28.7, "humidity": 60.5, "light": 420}`).Wait()
	waitFor(t, "sensor rows", func() bool {
		return count(t, pool, `SELECT count(*) FROM sensor_data`) == before+3
	})
	if n := count(t, pool, `SELECT count(*) FROM sensor_data d JOIN sensors s ON s.id = d.sensor_id
		WHERE d.id > $1 AND (s.type, d.value) IN (('temperature', 28.7), ('humidity', 60.5), ('light', 420))`,
		lastID); n != 3 {
		t.Errorf("stored values = %d", n)
	}

	result, err := devices.SendCommand(context.Background(), 1, model.DeviceID, model.CommandOn)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.ResultSuccess {
		t.Fatalf("command result = %+v", result)
	}
	if n := count(t, pool, `SELECT count(*) FROM devices WHERE status = 'ON'`); n != 1 {
		t.Error("device status not updated from the MQTT response")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
