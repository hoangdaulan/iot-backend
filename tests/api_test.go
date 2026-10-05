// Package tests exercises the HTTP API end to end over in-memory repositories and a fake ESP32.
// Integration tests against PostgreSQL and Mosquitto live in integration_test.go.
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"iot-backend/internal/model"
	"iot-backend/internal/repository/memory"
	"iot-backend/internal/router"
	"iot-backend/internal/service"
)

// fakeESP32 answers published commands the way the test configures.
type fakeESP32 struct {
	mu      sync.Mutex
	devices *service.DeviceService
	respond func(service.ControlMessage) *service.DeviceResponse
}

func (f *fakeESP32) PublishControl(_ context.Context, payload []byte) error {
	var msg service.ControlMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}
	f.mu.Lock()
	respond := f.respond
	f.mu.Unlock()
	if resp := respond(msg); resp != nil {
		go f.devices.HandleResponse(*resp)
	}
	return nil
}

func (f *fakeESP32) answer(result model.ActionResult, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond = func(m service.ControlMessage) *service.DeviceResponse {
		return &service.DeviceResponse{ActionID: m.ActionID, Status: result, Message: message}
	}
}

func (f *fakeESP32) stayQuiet() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond = func(service.ControlMessage) *service.DeviceResponse { return nil }
}

type harness struct {
	t       *testing.T
	handler http.Handler
	store   *memory.Store
	sensors *service.SensorService
	esp     *fakeESP32
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := memory.NewStore()
	tokens := service.NewTokenService("test-secret", time.Hour)
	auth := service.NewAuthService(store.Users(), tokens).WithBcryptCost(bcrypt.MinCost)
	sensors := service.NewSensorService(store.Sensors(), store.Devices())
	esp := &fakeESP32{}
	esp.answer(model.ResultSuccess, "")
	devices := service.NewDeviceService(store.Devices(), esp, 100*time.Millisecond, logger)
	esp.devices = devices

	// Seed accounts like the development seed.
	for _, u := range []struct {
		username, email string
		role            model.Role
		name            *string
	}{
		{"admin", "admin@myiot.local", model.RoleAdmin, ptr("Administrator")},
		{"user01", "user01@myiot.local", model.RoleUser, nil},
	} {
		hash, _ := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.MinCost)
		user := &model.User{Username: u.username, Email: u.email, PasswordHash: string(hash), Role: u.role, Name: u.name}
		if err := store.Users().Create(context.Background(), user); err != nil {
			t.Fatal(err)
		}
	}

	return &harness{
		t: t, store: store, sensors: sensors, esp: esp,
		handler: router.New(router.Deps{
			Auth: auth, Tokens: tokens, Sensors: sensors, Devices: devices,
			CORSAllowedOrigins: []string{"*"}, Logger: logger,
		}),
	}
}

type response struct {
	Code int
	Body []byte
}

func (r response) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
	return out
}

func (h *harness) do(method, path, token string, body any) response {
	h.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, req)
	return response{Code: w.Code, Body: w.Body.Bytes()}
}

func (h *harness) login(username string) string {
	h.t.Helper()
	res := h.do(http.MethodPost, "/api/auth/login", "", map[string]string{"username": username, "password": "123456"})
	if res.Code != http.StatusOK {
		h.t.Fatalf("login %s: %d %s", username, res.Code, res.Body)
	}
	return res.json(h.t)["accessToken"].(string)
}

func expect(t *testing.T, res response, code int, message string) {
	t.Helper()
	if res.Code != code {
		t.Fatalf("status = %d, want %d (%s)", res.Code, code, res.Body)
	}
	if message != "" {
		if got := res.json(t)["message"]; got != message {
			t.Errorf("message = %v, want %q", got, message)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// ── Auth ──

func TestRegister(t *testing.T) {
	h := newHarness(t)

	res := h.do(http.MethodPost, "/api/auth/register", "",
		map[string]string{"username": "user02", "email": "User02@Gmail.com", "password": "123456"})
	expect(t, res, http.StatusCreated, "Register successfully")
	user := res.json(t)["user"].(map[string]any)
	if user["username"] != "user02" || user["role"] != "USER" || user["id"] == nil {
		t.Errorf("user = %v", user)
	}
	if _, hasName := user["name"]; hasName {
		t.Error("name should be omitted when unset")
	}

	// The new account can log in, with its username or (case-insensitively) its email.
	h.login("user02")
	expect(t, h.do(http.MethodPost, "/api/auth/login", "",
		map[string]string{"username": "user02@gmail.com", "password": "123456"}), http.StatusOK, "")

	for name, body := range map[string]any{
		"duplicate username": map[string]string{"username": "admin", "email": "new@x.io", "password": "123456"},
		"duplicate email":    map[string]string{"username": "new", "email": "admin@myiot.local", "password": "123456"},
	} {
		t.Run(name, func(t *testing.T) {
			expect(t, h.do(http.MethodPost, "/api/auth/register", "", body),
				http.StatusConflict, "Username or email already exists")
		})
	}
	for name, body := range map[string]any{
		"short username": map[string]string{"username": "ab", "email": "a@x.io", "password": "123456"},
		"invalid email":  map[string]string{"username": "abc", "email": "not-an-email", "password": "123456"},
		"short password": map[string]string{"username": "abc", "email": "a@x.io", "password": "123"},
		"malformed json": `{"username":`,
	} {
		t.Run(name, func(t *testing.T) {
			expect(t, h.do(http.MethodPost, "/api/auth/register", "", body), http.StatusBadRequest, "")
		})
	}
}

func TestLogin(t *testing.T) {
	h := newHarness(t)

	res := h.do(http.MethodPost, "/api/auth/login", "", map[string]string{"username": "admin", "password": "123456"})
	expect(t, res, http.StatusOK, "")
	body := res.json(t)
	if token, _ := body["accessToken"].(string); strings.Count(token, ".") != 2 {
		t.Errorf("accessToken = %v", body["accessToken"])
	}
	// The partial user must match the Flutter UserSummary contract exactly.
	if user, _ := json.Marshal(body["user"]); string(user) != `{"id":1,"name":"Administrator","role":"ADMIN","username":"admin"}` {
		t.Errorf("user = %s", user)
	}

	t.Run("invalid password", func(t *testing.T) {
		expect(t, h.do(http.MethodPost, "/api/auth/login", "",
			map[string]string{"username": "admin", "password": "wrong-password"}),
			http.StatusUnauthorized, "Invalid email or password")
	})
	t.Run("unknown user", func(t *testing.T) {
		expect(t, h.do(http.MethodPost, "/api/auth/login", "",
			map[string]string{"username": "ghost", "password": "123456"}),
			http.StatusUnauthorized, "Invalid email or password")
	})
	t.Run("missing fields", func(t *testing.T) {
		expect(t, h.do(http.MethodPost, "/api/auth/login", "", map[string]string{"username": "admin"}),
			http.StatusBadRequest, "Invalid request data")
	})
}

func TestProfile(t *testing.T) {
	h := newHarness(t)

	t.Run("requires a valid token", func(t *testing.T) {
		expect(t, h.do(http.MethodGet, "/api/auth/profile", "", nil), http.StatusUnauthorized, "Unauthorized")
		expect(t, h.do(http.MethodGet, "/api/auth/profile", "forged.token.value", nil), http.StatusUnauthorized, "Unauthorized")
	})

	token := h.login("admin")
	res := h.do(http.MethodGet, "/api/auth/profile", token, nil)
	expect(t, res, http.StatusOK, "")
	profile := res.json(t)
	if profile["email"] != "admin@myiot.local" || profile["role"] != "ADMIN" || profile["username"] != "admin" {
		t.Errorf("profile = %v", profile)
	}
	if bytes.Contains(res.Body, []byte("password")) || bytes.Contains(res.Body, []byte("$2a$")) {
		t.Errorf("profile leaks the password hash: %s", res.Body)
	}

	t.Run("PATCH updates only the sent fields", func(t *testing.T) {
		res := h.do(http.MethodPatch, "/api/auth/profile", token, map[string]string{"phone": "0912345678"})
		expect(t, res, http.StatusOK, "")
		updated := res.json(t)
		if updated["phone"] != "0912345678" || updated["name"] != "Administrator" {
			t.Errorf("updated = %v", updated)
		}
	})

	t.Run("change password", func(t *testing.T) {
		expect(t, h.do(http.MethodPatch, "/api/auth/password", token,
			map[string]string{"oldPassword": "wrong", "newPassword": "abcdef"}),
			http.StatusBadRequest, "Old password is incorrect")
		expect(t, h.do(http.MethodPatch, "/api/auth/password", token,
			map[string]string{"oldPassword": "123456", "newPassword": "abc"}), http.StatusBadRequest, "")

		expect(t, h.do(http.MethodPatch, "/api/auth/password", token,
			map[string]string{"oldPassword": "123456", "newPassword": "new-secret"}), http.StatusNoContent, "")
		expect(t, h.do(http.MethodPost, "/api/auth/login", "",
			map[string]string{"username": "admin", "password": "123456"}), http.StatusUnauthorized, "")
		expect(t, h.do(http.MethodPost, "/api/auth/login", "",
			map[string]string{"username": "admin", "password": "new-secret"}), http.StatusOK, "")
	})
}

// ── Sensors ──

func TestSensorsList(t *testing.T) {
	h := newHarness(t)
	res := h.do(http.MethodGet, "/api/sensors", h.login("user01"), nil)
	expect(t, res, http.StatusOK, "")
	var sensors []map[string]any
	_ = json.Unmarshal(res.Body, &sensors)
	if len(sensors) != 3 || sensors[0]["type"] != "temperature" || sensors[1]["unit"] != "%" || sensors[2]["type"] != "light" {
		t.Errorf("sensors = %s", res.Body)
	}
}

func (h *harness) ingest(at time.Time, values map[model.SensorType]float64) {
	h.t.Helper()
	if _, err := h.sensors.Ingest(context.Background(), values, at); err != nil {
		h.t.Fatal(err)
	}
}

func TestSensorLatest(t *testing.T) {
	h := newHarness(t)
	token := h.login("admin")

	res := h.do(http.MethodGet, "/api/sensor-data/latest", token, nil)
	expect(t, res, http.StatusOK, "")
	if string(res.Body) != `{"data":{},"deviceStatus":"OFF","devices":[{"id":1,"name":"LED 1","type":"LED","status":"OFF"},{"id":2,"name":"LED 2","type":"LED","status":"OFF"},{"id":3,"name":"LED 3","type":"LED","status":"OFF"}]}` {
		t.Errorf("empty latest = %s", res.Body)
	}

	base := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	h.ingest(base, map[model.SensorType]float64{"temperature": 27, "humidity": 60, "light": 400})
	h.ingest(base.Add(time.Minute), map[model.SensorType]float64{"temperature": 28.7, "humidity": 60.5})

	res = h.do(http.MethodGet, "/api/sensor-data/latest", token, nil)
	expect(t, res, http.StatusOK, "")
	var latest struct {
		Data map[string]struct {
			ID        int64
			Value     float64
			Unit      string
			Timestamp time.Time
		}
		DeviceStatus string
	}
	_ = json.Unmarshal(res.Body, &latest)
	if latest.DeviceStatus != "OFF" || len(latest.Data) != 3 {
		t.Fatalf("latest = %s", res.Body)
	}
	if d := latest.Data["temperature"]; d.Value != 28.7 || d.Unit != "°C" || !d.Timestamp.Equal(base.Add(time.Minute)) {
		t.Errorf("temperature = %+v", d)
	}
	if d := latest.Data["light"]; d.Value != 400 || !d.Timestamp.Equal(base) {
		t.Errorf("light = %+v", d)
	}
}

type historyBody struct {
	Data          map[string][]struct{ Value float64 }
	Page          int
	PageSize      int
	TotalElements int64
	TotalPages    int
}

func TestSensorHistory(t *testing.T) {
	h := newHarness(t)
	token := h.login("admin")
	day := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	for i := range 3 {
		h.ingest(day.Add(time.Duration(i)*24*time.Hour),
			map[model.SensorType]float64{"temperature": 20 + float64(i), "humidity": 60 + float64(i), "light": 400})
	}

	get := func(query string) historyBody {
		t.Helper()
		res := h.do(http.MethodGet, "/api/sensor-data/history"+query, token, nil)
		expect(t, res, http.StatusOK, "")
		var body historyBody
		_ = json.Unmarshal(res.Body, &body)
		return body
	}

	t.Run("groups every type and pages 0-based by default", func(t *testing.T) {
		b := get("")
		if b.Page != 0 || b.PageSize != 20 || b.TotalElements != 9 || b.TotalPages != 1 {
			t.Errorf("paging = %+v", b)
		}
		if temps := b.Data["temperature"]; len(temps) != 3 || temps[0].Value != 22 {
			t.Errorf("temperature newest first = %+v", temps)
		}
	})
	t.Run("filters by type", func(t *testing.T) {
		b := get("?type=humidity")
		if b.TotalElements != 3 || len(b.Data["humidity"]) != 3 || len(b.Data["temperature"]) != 0 {
			t.Errorf("body = %+v", b)
		}
		if _, ok := b.Data["light"]; !ok {
			t.Error("all type keys are always present")
		}
	})
	t.Run("filters by timeRange", func(t *testing.T) {
		b := get("?timeRange=2026-08-15T00:00:00Z/..")
		if b.TotalElements != 6 {
			t.Errorf("open end: total = %d", b.TotalElements)
		}
		b = get("?type=temperature&timeRange=2026-08-14T00:00:00Z/2026-08-15T00:00:00Z")
		if b.TotalElements != 2 {
			t.Errorf("closed range: total = %d", b.TotalElements)
		}
	})
	t.Run("filters by value", func(t *testing.T) {
		if b := get("?value=400"); b.TotalElements != 3 || len(b.Data["light"]) != 3 {
			t.Errorf("body = %+v", b)
		}
	})
	t.Run("pages", func(t *testing.T) {
		b := get("?page=1&size=4")
		count := 0
		for _, entries := range b.Data {
			count += len(entries)
		}
		if b.Page != 1 || b.PageSize != 4 || b.TotalElements != 9 || b.TotalPages != 3 || count != 4 {
			t.Errorf("body = %+v (rows %d)", b, count)
		}
	})
	for _, query := range []string{"?type=co2", "?size=5001", "?page=-1", "?page=x",
		"?timeRange=yesterday", "?timeRange=2026-08-15T00:00:00Z/2026-08-14T00:00:00Z", "?value=abc"} {
		t.Run("rejects "+query, func(t *testing.T) {
			expect(t, h.do(http.MethodGet, "/api/sensor-data/history"+query, token, nil), http.StatusBadRequest, "")
		})
	}
}

// ── Devices ──

func TestDeviceCommand(t *testing.T) {
	h := newHarness(t)
	token := h.login("user01")
	command := func(path, body string) response {
		return h.do(http.MethodPost, path, token, body)
	}

	t.Run("success", func(t *testing.T) {
		res := command("/api/devices/1/command", `{"command":"ON"}`)
		expect(t, res, http.StatusOK, "")
		if string(res.Body) != `{"deviceId":1,"command":"ON","status":"SUCCESS","message":"Device turned on successfully"}` {
			t.Errorf("body = %s", res.Body)
		}
		latest := h.do(http.MethodGet, "/api/sensor-data/latest", token, nil).json(t)
		if latest["deviceStatus"] != "ON" {
			t.Errorf("LED status after success = %v", latest["deviceStatus"])
		}
	})

	t.Run("failed", func(t *testing.T) {
		h.esp.answer(model.ResultFailed, "LED driver fault")
		res := command("/api/devices/1/command", `{"command":"OFF"}`)
		expect(t, res, http.StatusOK, "LED driver fault")
		if res.json(t)["status"] != "FAILED" {
			t.Errorf("body = %s", res.Body)
		}
		if h.do(http.MethodGet, "/api/sensor-data/latest", token, nil).json(t)["deviceStatus"] != "ON" {
			t.Error("a failed command must not change the LED status")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		h.esp.stayQuiet()
		res := command("/api/devices/1/command", `{"command":"OFF"}`)
		expect(t, res, http.StatusGatewayTimeout, "Device did not respond in time")
		if string(res.Body) != `{"deviceId":1,"command":"OFF","status":"TIMEOUT","message":"Device did not respond in time"}` {
			t.Errorf("body = %s", res.Body)
		}
	})

	t.Run("unknown device", func(t *testing.T) {
		expect(t, command("/api/devices/4/command", `{"command":"ON"}`), http.StatusNotFound, "Device not found")
		expect(t, command("/api/devices/abc/command", `{"command":"ON"}`), http.StatusNotFound, "Device not found")
	})
	t.Run("invalid command", func(t *testing.T) {
		expect(t, command("/api/devices/1/command", `{"command":"BLINK"}`), http.StatusBadRequest, "Invalid command")
		expect(t, command("/api/devices/1/command", `{}`), http.StatusBadRequest, "Invalid command")
	})
	t.Run("requires a token", func(t *testing.T) {
		expect(t, h.do(http.MethodPost, "/api/devices/1/command", "", `{"command":"ON"}`), http.StatusUnauthorized, "")
	})
}

func TestDeviceHistory(t *testing.T) {
	h := newHarness(t)
	token := h.login("admin")
	h.do(http.MethodPost, "/api/devices/1/command", token, `{"command":"ON"}`)
	h.esp.answer(model.ResultFailed, "")
	h.do(http.MethodPost, "/api/devices/1/command", token, `{"command":"OFF"}`)

	res := h.do(http.MethodGet, "/api/devices/control-history", token, nil)
	expect(t, res, http.StatusOK, "")
	var page struct {
		Content                []map[string]any
		Page, Size, TotalPages int
		TotalElements          int64
	}
	_ = json.Unmarshal(res.Body, &page)
	if page.Page != 0 || page.Size != 20 || page.TotalElements != 2 || page.TotalPages != 1 || len(page.Content) != 2 {
		t.Fatalf("page = %s", res.Body)
	}
	newest, oldest := page.Content[0], page.Content[1]
	if newest["action"] != "TURN_OFF" || newest["result"] != "FAILED" || newest["deviceName"] != "LED 1" ||
		newest["message"] != "Device failed to turn off" {
		t.Errorf("newest = %v", newest)
	}
	if _, hasStatus := newest["status"]; hasStatus {
		t.Error("a failed action has no resulting device status")
	}
	if oldest["action"] != "TURN_ON" || oldest["result"] != "SUCCESS" || oldest["status"] != "ON" || oldest["deviceId"] != 1.0 {
		t.Errorf("oldest = %v", oldest)
	}

	t.Run("pages and filters", func(t *testing.T) {
		var p struct {
			Content    []map[string]any
			TotalPages int
		}
		_ = json.Unmarshal(h.do(http.MethodGet, "/api/devices/control-history?page=1&size=1&deviceId=1", token, nil).Body, &p)
		if len(p.Content) != 1 || p.TotalPages != 2 || p.Content[0]["action"] != "TURN_ON" {
			t.Errorf("page 1 = %+v", p)
		}
		future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		_ = json.Unmarshal(h.do(http.MethodGet, "/api/devices/control-history?from="+future, token, nil).Body, &p)
		if len(p.Content) != 0 {
			t.Errorf("future window = %+v", p)
		}
		expect(t, h.do(http.MethodGet, "/api/devices/control-history?from=bad", token, nil), http.StatusBadRequest, "")
	})
}

func TestCORSPreflight(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/auth/login", nil)
	req.Header.Set("Origin", "http://localhost:5555")
	req.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5555" {
		t.Errorf("preflight = %d %v", w.Code, w.Header())
	}
}
