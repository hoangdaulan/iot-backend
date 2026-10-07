// Package tests exercises the HTTP API end to end over in-memory repositories and a fake ESP32.
// Integration tests against PostgreSQL and Mosquitto live in integration_test.go.
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	uploads string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := memory.NewStore()
	tokens := service.NewTokenService("test-secret", time.Hour)
	uploads := t.TempDir()
	auth := service.NewAuthService(store.Users(), tokens).WithBcryptCost(bcrypt.MinCost).WithUploadDir(uploads)
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
		t: t, store: store, sensors: sensors, esp: esp, uploads: uploads,
		handler: router.New(router.Deps{
			Auth: auth, Tokens: tokens, Sensors: sensors, Devices: devices,
			CORSAllowedOrigins: []string{"*"}, UploadDir: uploads, Logger: logger,
		}),
	}
}

// upload posts one multipart file field.
func (h *harness) upload(path, token, field, filename string, data []byte) response {
	h.t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if field != "" {
		part, _ := w.CreateFormFile(field, filename)
		_, _ = part.Write(data)
	}
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return response{Code: rec.Code, Body: rec.Body.Bytes()}
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

	t.Run("stores the full name", func(t *testing.T) {
		res := h.do(http.MethodPost, "/api/auth/register", "", map[string]string{
			"name": "  Tran Thi B ", "username": "user03", "email": "user03@x.io", "password": "123456",
		})
		expect(t, res, http.StatusCreated, "")
		if got := res.json(t)["user"].(map[string]any)["name"]; got != "Tran Thi B" {
			t.Errorf("name = %v", got)
		}
		if got := h.do(http.MethodGet, "/api/auth/profile", h.login("user03"), nil).json(t)["name"]; got != "Tran Thi B" {
			t.Errorf("profile name = %v", got)
		}
	})

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

	t.Run("PATCH updates swagger and the other editable fields", func(t *testing.T) {
		res := h.do(http.MethodPatch, "/api/auth/profile", token, map[string]string{
			"name": "New Name", "github": "https://github.com/x", "figma": "https://figma.com/@x",
			"swagger": "https://example.com/swagger",
		})
		expect(t, res, http.StatusOK, "")
		updated := res.json(t)
		if updated["name"] != "New Name" || updated["github"] != "https://github.com/x" ||
			updated["figma"] != "https://figma.com/@x" || updated["swagger"] != "https://example.com/swagger" {
			t.Errorf("updated = %v", updated)
		}
		if got := h.do(http.MethodGet, "/api/auth/profile", token, nil).json(t)["swagger"]; got != "https://example.com/swagger" {
			t.Errorf("profile swagger = %v", got)
		}
	})

	t.Run("PATCH cannot change the email or username", func(t *testing.T) {
		res := h.do(http.MethodPatch, "/api/auth/profile", token, map[string]string{
			"email": "hacker@x.io", "username": "hacker", "role": "USER", "phone": "111",
		})
		expect(t, res, http.StatusOK, "")
		updated := res.json(t)
		if updated["email"] != "admin@myiot.local" || updated["username"] != "admin" || updated["role"] != "ADMIN" {
			t.Errorf("identity changed: %v", updated)
		}
		if updated["phone"] != "111" {
			t.Errorf("phone = %v", updated["phone"])
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

func TestAvatarUpload(t *testing.T) {
	h := newHarness(t)
	token := h.login("user01")
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	jpeg := append([]byte("\xff\xd8\xff\xe0"), make([]byte, 64)...)

	res := h.upload("/api/auth/avatar", token, "file", "me.png", png)
	expect(t, res, http.StatusOK, "")
	first, _ := res.json(t)["avatar"].(string)
	if !strings.HasPrefix(first, "/uploads/avatars/") || !strings.HasSuffix(first, ".png") {
		t.Fatalf("avatar = %q", first)
	}
	if got := h.do(http.MethodGet, "/api/auth/profile", token, nil).json(t)["avatar"]; got != first {
		t.Errorf("profile avatar = %v, want %s", got, first)
	}

	t.Run("is served without a token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, first, nil)
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), png) {
			t.Errorf("GET %s = %d, %d bytes", first, rec.Code, rec.Body.Len())
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Error("uploads must be served with nosniff")
		}
	})

	t.Run("a new upload replaces the old file", func(t *testing.T) {
		res := h.upload("/api/auth/avatar", token, "file", "me.jpg", jpeg)
		expect(t, res, http.StatusOK, "")
		second, _ := res.json(t)["avatar"].(string)
		if second == first || !strings.HasSuffix(second, ".jpg") {
			t.Errorf("second avatar = %q", second)
		}
		if _, err := os.Stat(filepath.Join(h.uploads, "avatars", filepath.Base(first))); !os.IsNotExist(err) {
			t.Errorf("old avatar file still exists (err = %v)", err)
		}
		if _, err := os.Stat(filepath.Join(h.uploads, "avatars", filepath.Base(second))); err != nil {
			t.Errorf("new avatar file missing: %v", err)
		}
	})

	t.Run("rejects bad uploads", func(t *testing.T) {
		expect(t, h.upload("/api/auth/avatar", token, "file", "x.png", []byte("<script>alert(1)</script>")),
			http.StatusBadRequest, "Avatar must be a PNG, JPEG, GIF or WebP image")
		expect(t, h.upload("/api/auth/avatar", token, "file", "x.png", nil), http.StatusBadRequest, "")
		expect(t, h.upload("/api/auth/avatar", token, "other", "x.png", png), http.StatusBadRequest, "")
		big := append(append([]byte{}, png...), make([]byte, service.MaxAvatarBytes)...)
		expect(t, h.upload("/api/auth/avatar", token, "file", "big.png", big), http.StatusBadRequest, "")
	})

	t.Run("requires a token", func(t *testing.T) {
		expect(t, h.upload("/api/auth/avatar", "", "file", "me.png", png), http.StatusUnauthorized, "")
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
	t.Run("searches by filter and q", func(t *testing.T) {
		cases := []struct {
			query string
			total int64
		}{
			{"?filter=all&q=whatever", 0},
			{"?filter=all", 9},
			{"?q=hum", 3},
			{"?filter=all&q=hum", 3},
			{"?filter=all&q=light", 3},
			{"?filter=all&q=2", 3},
			{"?filter=all&q=1", 3},
			{"?filter=all&q=61", 1},
			{"?filter=all&q=22", 1},
			{"?filter=all&q=400", 3},
			{"?filter=all&q=2026", 9},
			{"?filter=all&q=2026/08/15", 3},
			{"?filter=all&q=2026/08/15%2007&utcOffset=420", 3},
			{"?filter=sensor&q=hum", 3},
			{"?filter=sensor&q=3", 3},
			{"?filter=sensor&q=", 9},
			{"?filter=sensor&q=nothing", 0},
			{"?filter=temperature", 3},
			{"?filter=temperature&q=21", 1},
			{"?filter=temperature&q=21.0", 1},
			{"?filter=temperature&q=21.5", 0},
			{"?filter=temperature&q=2", 0},
			{"?filter=humidity&q=6", 0},
			{"?filter=humidity&q=61", 1},
			{"?filter=humidity&q=61.1", 0},
			{"?filter=temperature&q=-21", 0},
			{"?filter=humidity&q=60", 1},
			{"?filter=light&q=400", 3},
			{"?filter=time&q=2026", 9},
			{"?filter=time&q=2026/08", 9},
			{"?filter=time&q=2026/08/15", 3},
			{"?filter=time&q=2026/08/15%2000", 3},
			{"?filter=time&q=2026/08/15%2001", 0},
			{"?filter=time&q=2026/08/15%2000:00:00", 3},
			{"?filter=time&q=2026/08/15%2000:00:01", 0},
			{"?filter=time&q=2026/08/15%2007&utcOffset=420", 3},
			{"?filter=time&q=2026/08/15&utcOffset=420", 3},
			{"?filter=time&q=2026/08/14&utcOffset=420", 3},
			{"?filter=time&q=2026/08/15&utcOffset=-60", 3},
			{"?filter=time&q=2026/08/16&utcOffset=-60", 0},
			{"?filter=time&q=2026-08-15", 3},
			{"?filter=time&q=", 9},
			{"?filter=time&q=2026/08/14&timeRange=2026-08-14T12:00:00Z/..", 0},
		}
		for _, tc := range cases {
			if b := get(tc.query); b.TotalElements != tc.total {
				t.Errorf("%s: total = %d, want %d", tc.query, b.TotalElements, tc.total)
			}
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
		"?timeRange=yesterday", "?filter=co2", "?filter=temperature&q=abc", "?filter=time&q=tomorrow",
		"?filter=time&q=2026/13", "?filter=time&q=2026/02/30", "?filter=time&q=2026/08/15%2025", "?utcOffset=x", "?timeRange=2026-08-15T00:00:00Z/2026-08-14T00:00:00Z", "?value=abc"} {
		t.Run("rejects "+query, func(t *testing.T) {
			expect(t, h.do(http.MethodGet, "/api/sensor-data/history"+query, token, nil), http.StatusBadRequest, "")
		})
	}
}

// ── Devices ──

func TestSensorHistoryBucket(t *testing.T) {
	h := newHarness(t)
	token := h.login("admin")
	base := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	// Temperature 20 and 22 share the 10:00 window, 24 is alone in the 10:05 window.
	for _, r := range []struct {
		at   time.Duration
		temp float64
	}{{10 * time.Second, 20}, {2 * time.Minute, 22}, {7 * time.Minute, 24}} {
		h.ingest(base.Add(r.at), map[model.SensorType]float64{"temperature": r.temp, "light": r.temp * 10})
	}

	type entry struct {
		Value     float64
		Timestamp time.Time
	}
	var body struct {
		Data          map[string][]entry
		TotalElements int64
	}
	get := func(query string) response {
		res := h.do(http.MethodGet, "/api/sensor-data/history"+query, token, nil)
		_ = json.Unmarshal(res.Body, &body)
		return res
	}

	expect(t, get("?bucket=5m&type=temperature"), http.StatusOK, "")
	temps := body.Data["temperature"]
	if body.TotalElements != 2 || len(temps) != 2 {
		t.Fatalf("buckets = %+v (total %d)", temps, body.TotalElements)
	}
	if !temps[0].Timestamp.Equal(base.Add(5*time.Minute)) || temps[0].Value != 24 ||
		!temps[1].Timestamp.Equal(base) || temps[1].Value != 21 {
		t.Errorf("newest first, window start, average: %+v", temps)
	}

	t.Run("buckets every sensor separately", func(t *testing.T) {
		expect(t, get("?bucket=5m"), http.StatusOK, "")
		if body.TotalElements != 4 || len(body.Data["light"]) != 2 || body.Data["light"][1].Value != 210 {
			t.Errorf("total %d, light %+v", body.TotalElements, body.Data["light"])
		}
	})
	t.Run("a wider window merges them and pages count windows", func(t *testing.T) {
		expect(t, get("?bucket=10m&type=temperature"), http.StatusOK, "")
		if body.TotalElements != 1 || body.Data["temperature"][0].Value != 22 {
			t.Errorf("total %d, %+v", body.TotalElements, body.Data["temperature"])
		}
		expect(t, get("?bucket=5m&type=temperature&size=1&page=1"), http.StatusOK, "")
		if len(body.Data["temperature"]) != 1 || body.Data["temperature"][0].Value != 21 {
			t.Errorf("page 1 = %+v", body.Data["temperature"])
		}
	})
	t.Run("respects the time range", func(t *testing.T) {
		expect(t, get("?bucket=5m&type=temperature&timeRange=2026-10-06T10:05:00Z/.."), http.StatusOK, "")
		if body.TotalElements != 1 {
			t.Errorf("total = %d", body.TotalElements)
		}
	})
	for _, query := range []string{"?bucket=30s", "?bucket=90s", "?bucket=25h", "?bucket=abc", "?bucket=-5m"} {
		t.Run("rejects "+query, func(t *testing.T) {
			expect(t, h.do(http.MethodGet, "/api/sensor-data/history"+query, token, nil), http.StatusBadRequest, "")
		})
	}
}

func TestSensorHistoryValueSearch(t *testing.T) {
	h := newHarness(t)
	token := h.login("admin")
	at := time.Date(2026, 10, 6, 11, 0, 0, 0, time.UTC)
	for i, temp := range []float64{28, 28.5, 28.99, 29, 27.99, -3.5, -3, -4} {
		h.ingest(at.Add(time.Duration(i)*time.Minute), map[model.SensorType]float64{"temperature": temp})
	}

	for _, tc := range []struct {
		query string
		total int64
	}{
		{"28", 3},    // integer part 28: 28, 28.5, 28.99
		{"28.5", 1},  // 28.5 up to 28.6
		{"28.9", 1},  // 28.99
		{"28.99", 1}, // exact to two decimals
		{"29", 1},    // 29 only; 28.99 is part of 28
		{"27", 1},    // 27.99
		{"-3", 2},    // integer part -3: -3.5, -3
		{"-4", 1},    // -4
		{"-3.5", 1},  // -3.5 down to -3.59
		{"5", 0},
	} {
		res := h.do(http.MethodGet, "/api/sensor-data/history?filter=temperature&q="+tc.query, token, nil)
		expect(t, res, http.StatusOK, "")
		var b historyBody
		_ = json.Unmarshal(res.Body, &b)
		if b.TotalElements != tc.total {
			t.Errorf("q=%s: total = %d, want %d", tc.query, b.TotalElements, tc.total)
		}
	}
}

func TestDeviceList(t *testing.T) {
	h := newHarness(t)
	token := h.login("user01")

	res := h.do(http.MethodGet, "/api/devices", token, nil)
	expect(t, res, http.StatusOK, "")
	want := `[{"id":1,"name":"LED 1","type":"LED","status":"OFF"},{"id":2,"name":"LED 2","type":"LED","status":"OFF"},{"id":3,"name":"LED 3","type":"LED","status":"OFF"}]`
	if string(res.Body) != want {
		t.Errorf("body = %s", res.Body)
	}
	expect(t, h.do(http.MethodGet, "/api/devices", "", nil), http.StatusUnauthorized, "")
}

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

func TestDeviceHistorySearch(t *testing.T) {
	h := newHarness(t)
	token := h.login("admin")
	h.do(http.MethodPost, "/api/devices/1/command", token, `{"command":"ON"}`)
	h.do(http.MethodPost, "/api/devices/2/command", token, `{"command":"ON"}`)
	h.esp.answer(model.ResultFailed, "")
	h.do(http.MethodPost, "/api/devices/1/command", token, `{"command":"OFF"}`)
	now := time.Now().UTC()

	total := func(query string) int64 {
		t.Helper()
		res := h.do(http.MethodGet, "/api/devices/control-history"+query, token, nil)
		expect(t, res, http.StatusOK, "")
		var p struct{ TotalElements int64 }
		_ = json.Unmarshal(res.Body, &p)
		return p.TotalElements
	}
	for _, tc := range []struct {
		query string
		want  int64
	}{
		{"", 3},
		{"?deviceId=1", 2},
		{"?deviceId=2", 1},
		{"?deviceId=3", 0},
		{"?action=TURN_ON", 2},
		{"?action=TURN_OFF", 1},
		{"?result=SUCCESS", 2},
		{"?result=FAILED", 1},
		{"?result=TIMEOUT", 0},
		{"?deviceId=1&action=TURN_ON&result=SUCCESS", 1},
		{"?q=" + now.Format("2006"), 3},
		{"?q=" + now.Format("2006/01/02"), 3},
		{"?q=" + now.Format("2006/01/02") + "%20" + now.Format("15") + "&utcOffset=0", 3},
		{"?q=" + now.Add(-48*time.Hour).Format("2006/01/02"), 0},
		{"?q=" + now.Format("2006/01/02") + "&deviceId=2", 1},
		{"?q=", 3},
	} {
		if got := total(tc.query); got != tc.want {
			t.Errorf("%s: total = %d, want %d", tc.query, got, tc.want)
		}
	}
	for _, query := range []string{"?action=BLINK", "?result=DONE", "?utcOffset=x", "?q=led", "?q=2026/13"} {
		expect(t, h.do(http.MethodGet, "/api/devices/control-history"+query, token, nil), http.StatusBadRequest, "")
	}
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
