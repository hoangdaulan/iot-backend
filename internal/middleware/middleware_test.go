package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"iot-backend/internal/model"
	"iot-backend/internal/service"
)

func newEngine(tokens *service.TokenService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/protected", Auth(tokens), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"userId": ClaimsFrom(c).UserID, "role": ClaimsFrom(c).Role})
	})
	return r
}

func get(r *gin.Engine, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAuth(t *testing.T) {
	tokens := service.NewTokenService("test-secret", time.Hour)
	r := newEngine(tokens)
	valid, err := tokens.Issue(&model.User{ID: 7, Role: model.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("accepts a valid bearer token and exposes its claims", func(t *testing.T) {
		w := get(r, "Bearer "+valid)
		if w.Code != http.StatusOK || w.Body.String() != `{"role":"ADMIN","userId":7}` {
			t.Errorf("got %d %s", w.Code, w.Body)
		}
	})

	expired, _ := service.NewTokenService("test-secret", -time.Minute).Issue(&model.User{ID: 7})
	otherKey, _ := service.NewTokenService("other-secret", time.Hour).Issue(&model.User{ID: 7})
	for name, header := range map[string]string{
		"missing header":    "",
		"no bearer scheme":  valid,
		"wrong scheme":      "Basic " + valid,
		"empty token":       "Bearer ",
		"garbage token":     "Bearer not.a.jwt",
		"expired token":     "Bearer " + expired,
		"wrong signing key": "Bearer " + otherKey,
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			w := get(r, header)
			if w.Code != http.StatusUnauthorized || w.Body.String() != `{"message":"Unauthorized"}` {
				t.Errorf("got %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestCORS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORS([]string{"http://localhost:5000"}))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("Origin", "http://localhost:5000")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5000" {
		t.Errorf("preflight: %d %v", w.Code, w.Header())
	}

	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "http://evil.example")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("disallowed origin must not be echoed")
	}
}
