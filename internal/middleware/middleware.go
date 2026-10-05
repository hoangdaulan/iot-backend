// Package middleware provides JWT authentication and CORS for the Gin router.
package middleware

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"iot-backend/internal/dto"
	"iot-backend/internal/service"
)

const claimsKey = "claims"

// TokenParser verifies an access token.
type TokenParser interface {
	Parse(raw string) (service.Claims, error)
}

// Auth rejects requests without a valid "Authorization: Bearer <token>" header with 401.
func Auth(tokens TokenParser) gin.HandlerFunc {
	return func(c *gin.Context) {
		scheme, raw, ok := strings.Cut(c.GetHeader("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(raw) == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, dto.ErrorResponse{Message: "Unauthorized"})
			return
		}
		claims, err := tokens.Parse(strings.TrimSpace(raw))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, dto.ErrorResponse{Message: "Unauthorized"})
			return
		}
		c.Set(claimsKey, claims)
		c.Next()
	}
}

// ClaimsFrom returns the claims set by Auth.
func ClaimsFrom(c *gin.Context) service.Claims {
	claims, _ := c.Get(claimsKey)
	return claims.(service.Claims)
}

// CORS allows browser clients (Flutter Web) from the given origins; "*" allows any origin.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowAll := slices.Contains(allowedOrigins, "*")
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowAll || slices.Contains(allowedOrigins, origin)) {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
