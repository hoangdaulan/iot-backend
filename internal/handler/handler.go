// Package handler adapts HTTP requests to the services.
package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"iot-backend/internal/apperr"
	"iot-backend/internal/dto"
)

// respondError writes {"message": ...} with the error's status, or 500 for unexpected errors.
func respondError(c *gin.Context, logger *slog.Logger, err error) {
	if appErr, ok := apperr.As(err); ok {
		c.JSON(appErr.Status, dto.ErrorResponse{Message: appErr.Message})
		return
	}
	logger.Error("request failed", "method", c.Request.Method, "path", c.FullPath(), "error", err)
	c.JSON(http.StatusInternalServerError, dto.ErrorResponse{Message: "Internal server error"})
}

func bindJSON(c *gin.Context, dst any) error {
	if err := c.ShouldBindJSON(dst); err != nil {
		return apperr.BadRequest("Invalid request data")
	}
	return nil
}

// queryInt parses an optional integer query parameter.
func queryInt(c *gin.Context, key string) (int, error) {
	raw := c.Query(key)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperr.BadRequest("Invalid query parameters: " + key + " must be an integer")
	}
	return v, nil
}

// queryTime parses an optional time query parameter.
func queryTime(c *gin.Context, key string) (*time.Time, error) {
	raw := c.Query(key)
	if raw == "" {
		return nil, nil
	}
	t, err := parseTime(raw)
	if err != nil {
		return nil, apperr.BadRequest("Invalid query parameters: " + key + " must be an ISO-8601 time")
	}
	return &t, nil
}

// parseTimeRange parses an ISO-8601 interval "<from>/<to>" where ".." or an empty side means
// open-ended.
func parseTimeRange(raw string) (from, to *time.Time, err error) {
	start, end, ok := strings.Cut(raw, "/")
	if !ok {
		return nil, nil, apperr.BadRequest("Invalid query parameters: timeRange must be <from>/<to>")
	}
	parse := func(side string) (*time.Time, error) {
		if side == "" || side == ".." {
			return nil, nil
		}
		t, err := parseTime(side)
		if err != nil {
			return nil, apperr.BadRequest("Invalid query parameters: timeRange bounds must be ISO-8601 times")
		}
		return &t, nil
	}
	if from, err = parse(start); err != nil {
		return nil, nil, err
	}
	if to, err = parse(end); err != nil {
		return nil, nil, err
	}
	return from, to, nil
}

// parseTime accepts RFC 3339 times, plus zone-less times and dates, which are read as UTC.
func parseTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, apperr.BadRequest("invalid time")
}
