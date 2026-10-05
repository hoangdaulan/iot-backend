package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"iot-backend/internal/apperr"
	"iot-backend/internal/dto"
	"iot-backend/internal/middleware"
	"iot-backend/internal/model"
	"iot-backend/internal/service"
)

type DeviceHandler struct {
	devices *service.DeviceService
	logger  *slog.Logger
}

func NewDeviceHandler(devices *service.DeviceService, logger *slog.Logger) *DeviceHandler {
	return &DeviceHandler{devices: devices, logger: logger}
}

// Command handles POST /api/devices/:id/command. The response body is always a
// DeviceCommandResult: 200 for SUCCESS and FAILED, 504 for TIMEOUT.
func (h *DeviceHandler) Command(c *gin.Context) {
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		respondError(c, h.logger, apperr.NotFound("Device not found"))
		return
	}
	var req dto.DeviceCommandRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.logger, apperr.BadRequest("Invalid command"))
		return
	}
	result, err := h.devices.SendCommand(c.Request.Context(), middleware.ClaimsFrom(c).UserID,
		deviceID, req.Command)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	status := http.StatusOK
	if result.Status == model.ResultTimeout {
		status = http.StatusGatewayTimeout
	}
	c.JSON(status, dto.DeviceCommandResult{
		DeviceID: result.DeviceID, Command: result.Command, Status: result.Status, Message: result.Message,
	})
}

// History handles GET /api/devices/control-history?deviceId=&from=&to=&page=&size=.
func (h *DeviceHandler) History(c *gin.Context) {
	var q service.ActionHistoryQuery
	var err error
	if raw := c.Query("deviceId"); raw != "" {
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil {
			respondError(c, h.logger, apperr.BadRequest("Invalid query parameters: deviceId must be an integer"))
			return
		}
		q.DeviceID = &id
	}
	if q.From, err = queryTime(c, "from"); err == nil {
		q.To, err = queryTime(c, "to")
	}
	if err == nil {
		q.Page, err = queryInt(c, "page")
	}
	if err == nil {
		q.Size, err = queryInt(c, "size")
	}
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	if q.Size == 0 {
		q.Size = service.DefaultPageSize
	}

	items, total, err := h.devices.History(c.Request.Context(), q)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	content := make([]dto.DeviceActionHistoryItem, 0, len(items))
	for _, item := range items {
		content = append(content, dto.HistoryItemFrom(item))
	}
	c.JSON(http.StatusOK, dto.PageResponse[dto.DeviceActionHistoryItem]{
		Content: content, Page: q.Page, Size: q.Size,
		TotalElements: total, TotalPages: dto.TotalPages(total, q.Size),
	})
}
