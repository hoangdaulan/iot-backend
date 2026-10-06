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

// List handles GET /api/devices.
// @Summary List devices
// @Tags Devices
// @Produce json
// @Security BearerAuth
// @Success 200 {array} dto.DeviceSummary
// @Failure 401 {object} dto.ErrorResponse
// @Router /devices [get]
func (h *DeviceHandler) List(c *gin.Context) {
	devices, err := h.devices.List(c.Request.Context())
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	out := make([]dto.DeviceSummary, 0, len(devices))
	for _, d := range devices {
		out = append(out, dto.DeviceSummary{ID: d.ID, Name: d.Name, Type: d.Type, Status: d.Status})
	}
	c.JSON(http.StatusOK, out)
}

// Command handles POST /api/devices/:id/command. The response body is always a
// DeviceCommandResult: 200 for SUCCESS and FAILED, 504 for TIMEOUT.
// @Summary Send a command to a device
// @Description Always returns a DeviceCommandResult: 200 for SUCCESS and FAILED, 504 for TIMEOUT.
// @Tags Devices
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body dto.DeviceCommandRequest true "Request body"
// @Param id path int true "Device ID"
// @Success 200 {object} dto.DeviceCommandResult
// @Failure 400 {object} dto.ErrorResponse
// @Failure 401 {object} dto.ErrorResponse
// @Failure 404 {object} dto.ErrorResponse
// @Failure 504 {object} dto.ErrorResponse
// @Router /devices/{id}/command [post]
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

// History handles GET /api/devices/control-history?deviceId=&action=&result=&q=&utcOffset=&from=&to=&page=&size=.
// @Summary Device control history
// @Tags Devices
// @Produce json
// @Security BearerAuth
// @Param deviceId query int false "Device ID"
// @Param action query string false "TURN_ON or TURN_OFF"
// @Param result query string false "PENDING, SUCCESS, FAILED or TIMEOUT"
// @Param q query string false "Time search: a leading part of yyyy/MM/dd HH:mm:ss, e.g. 2026/10/06 11"
// @Param utcOffset query int false "Client offset east of UTC in minutes, used to read a time search (default 0)"
// @Param from query string false "ISO-8601 start time"
// @Param to query string false "ISO-8601 end time"
// @Param page query int false "0-based page"
// @Param size query int false "Page size"
// @Success 200 {object} dto.DeviceActionHistoryPage
// @Failure 400 {object} dto.ErrorResponse
// @Failure 401 {object} dto.ErrorResponse
// @Router /devices/control-history [get]
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
	if raw := c.Query("action"); raw != "" {
		action := model.ActionType(raw)
		q.Action = &action
	}
	if raw := c.Query("result"); raw != "" {
		result := model.ActionResult(raw)
		q.Result = &result
	}
	q.Query = c.Query("q")
	if q.UtcOffsetMinutes, err = queryInt(c, "utcOffset"); err != nil {
		respondError(c, h.logger, err)
		return
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
