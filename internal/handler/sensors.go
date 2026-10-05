package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"iot-backend/internal/apperr"
	"iot-backend/internal/dto"
	"iot-backend/internal/model"
	"iot-backend/internal/service"
)

type SensorHandler struct {
	sensors *service.SensorService
	logger  *slog.Logger
}

func NewSensorHandler(sensors *service.SensorService, logger *slog.Logger) *SensorHandler {
	return &SensorHandler{sensors: sensors, logger: logger}
}

// List handles GET /api/sensors.
func (h *SensorHandler) List(c *gin.Context) {
	sensors, err := h.sensors.Sensors(c.Request.Context())
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	out := make([]dto.Sensor, 0, len(sensors))
	for _, s := range sensors {
		out = append(out, dto.SensorFrom(s))
	}
	c.JSON(http.StatusOK, out)
}

// Latest handles GET /api/sensor-data/latest.
func (h *SensorHandler) Latest(c *gin.Context) {
	readings, status, err := h.sensors.Latest(c.Request.Context())
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	data := make(map[model.SensorType]dto.SensorDataEntry, len(readings))
	for _, r := range readings {
		data[r.Type] = dto.SensorDataEntryFrom(r)
	}
	c.JSON(http.StatusOK, dto.LatestSensorDataResponse{Data: data, DeviceStatus: status})
}

// History handles GET /api/sensor-data/history?type=&timeRange=&value=&page=&size=.
func (h *SensorHandler) History(c *gin.Context) {
	q, err := parseSensorHistoryQuery(c)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	readings, total, err := h.sensors.History(c.Request.Context(), q)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}

	// The report's grouped shape: every type is present, newest first within each.
	data := make(map[model.SensorType][]dto.SensorDataEntry, len(model.SensorTypes))
	for _, t := range model.SensorTypes {
		data[t] = []dto.SensorDataEntry{}
	}
	for _, r := range readings {
		data[r.Type] = append(data[r.Type], dto.SensorDataEntryFrom(r))
	}
	c.JSON(http.StatusOK, dto.SensorHistoryResponse{
		Data: data, Page: q.Page, PageSize: q.Size,
		TotalElements: total, TotalPages: dto.TotalPages(total, q.Size),
	})
}

func parseSensorHistoryQuery(c *gin.Context) (service.HistoryQuery, error) {
	var q service.HistoryQuery
	if raw := c.Query("type"); raw != "" {
		t := model.SensorType(raw)
		q.Type = &t
	}
	if raw := c.Query("timeRange"); raw != "" {
		from, to, err := parseTimeRange(raw)
		if err != nil {
			return q, err
		}
		q.From, q.To = from, to
	}
	if raw := c.Query("value"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return q, apperr.BadRequest("Invalid query parameters: value must be a number")
		}
		q.Value = &v
	}
	var err error
	if q.Page, err = queryInt(c, "page"); err != nil {
		return q, err
	}
	if q.Size, err = queryInt(c, "size"); err != nil {
		return q, err
	}
	// Validate here so the response echoes the effective page size.
	return q, q.Validate()
}
