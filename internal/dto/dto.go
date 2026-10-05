// Package dto defines the JSON request and response bodies. Field names and shapes match the
// Flutter client's DTOs (lib/data/models/ in the frontend).
package dto

import (
	"time"

	"iot-backend/internal/model"
)

type ErrorResponse struct {
	Message string `json:"message"`
}

// ── Auth ──

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// UserSummary is the partial user embedded in the login and register responses.
type UserSummary struct {
	ID       int64      `json:"id"`
	Username string     `json:"username"`
	Name     *string    `json:"name,omitempty"`
	Role     model.Role `json:"role"`
}

type LoginResponse struct {
	AccessToken string      `json:"accessToken"`
	User        UserSummary `json:"user"`
}

type RegisterRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type RegisterResponse struct {
	Message string      `json:"message"`
	User    UserSummary `json:"user"`
}

// User is the full profile; it never includes the password hash.
type User struct {
	ID       int64      `json:"id"`
	Name     *string    `json:"name,omitempty"`
	Email    string     `json:"email"`
	Username string     `json:"username"`
	Phone    *string    `json:"phone,omitempty"`
	Avatar   *string    `json:"avatar,omitempty"`
	Github   *string    `json:"github,omitempty"`
	Figma    *string    `json:"figma,omitempty"`
	Role     model.Role `json:"role"`
}

// UpdateProfileRequest is a partial update: absent fields are left unchanged.
type UpdateProfileRequest struct {
	Name   *string `json:"name"`
	Phone  *string `json:"phone"`
	Avatar *string `json:"avatar"`
	Github *string `json:"github"`
	Figma  *string `json:"figma"`
}

type ChangePasswordRequest struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// ── Sensors ──

type Sensor struct {
	ID        int64            `json:"id"`
	Name      string           `json:"name"`
	Type      model.SensorType `json:"type"`
	Unit      string           `json:"unit"`
	Status    *string          `json:"status,omitempty"`
	MQTTTopic *string          `json:"mqttTopic,omitempty"`
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// SensorDataEntry is one measurement; its type is given by the enclosing "data" key.
type SensorDataEntry struct {
	ID        int64     `json:"id"`
	Value     float64   `json:"value"`
	Unit      string    `json:"unit"`
	Timestamp time.Time `json:"timestamp"`
}

// LatestSensorDataResponse is the newest measurement per sensor type plus the LED status.
type LatestSensorDataResponse struct {
	Data         map[model.SensorType]SensorDataEntry `json:"data"`
	DeviceStatus model.DeviceStatus                   `json:"deviceStatus"`
}

// SensorHistoryResponse keeps the report's grouped-by-type shape. Paging is 0-based and the
// totals count individual measurements, like PageResponse.
type SensorHistoryResponse struct {
	Data          map[model.SensorType][]SensorDataEntry `json:"data"`
	Page          int                                    `json:"page"`
	PageSize      int                                    `json:"pageSize"`
	TotalElements int64                                  `json:"totalElements"`
	TotalPages    int                                    `json:"totalPages"`
}

// ── Devices ──

type DeviceCommandRequest struct {
	Command model.DeviceCommand `json:"command"`
}

type DeviceCommandResult struct {
	DeviceID int64               `json:"deviceId"`
	Command  model.DeviceCommand `json:"command"`
	Status   model.ActionResult  `json:"status"`
	Message  string              `json:"message"`
}

type DeviceActionHistoryItem struct {
	ID         int64               `json:"id"`
	DeviceID   int64               `json:"deviceId"`
	DeviceName string              `json:"deviceName"`
	Action     model.ActionType    `json:"action"`
	Status     *model.DeviceStatus `json:"status,omitempty"`
	Result     model.ActionResult  `json:"result"`
	Timestamp  time.Time           `json:"timestamp"`
	Message    *string             `json:"message,omitempty"`
}

type PageResponse[T any] struct {
	Content       []T   `json:"content"`
	Page          int   `json:"page"`
	Size          int   `json:"size"`
	TotalElements int64 `json:"totalElements"`
	TotalPages    int   `json:"totalPages"`
}

// TotalPages returns the number of pages of size needed for total rows.
func TotalPages(total int64, size int) int {
	if size <= 0 {
		return 0
	}
	return int((total + int64(size) - 1) / int64(size))
}

// ── Mapping ──

func UserFrom(u *model.User) User {
	return User{
		ID: u.ID, Name: u.Name, Email: u.Email, Username: u.Username, Phone: u.Phone,
		Avatar: u.Avatar, Github: u.Github, Figma: u.Figma, Role: u.Role,
	}
}

func UserSummaryFrom(u *model.User) UserSummary {
	return UserSummary{ID: u.ID, Username: u.Username, Name: u.Name, Role: u.Role}
}

func SensorFrom(s model.Sensor) Sensor {
	return Sensor{
		ID: s.ID, Name: s.Name, Type: s.Type, Unit: s.Unit, Status: s.Status,
		MQTTTopic: s.MQTTTopic, CreatedAt: s.CreatedAt.UTC(), UpdatedAt: s.UpdatedAt.UTC(),
	}
}

func SensorDataEntryFrom(r model.SensorReading) SensorDataEntry {
	return SensorDataEntry{ID: r.ID, Value: r.Value, Unit: r.Unit, Timestamp: r.Timestamp.UTC()}
}

func HistoryItemFrom(a model.DeviceActionHistory) DeviceActionHistoryItem {
	item := DeviceActionHistoryItem{
		ID: a.ID, DeviceID: a.DeviceID, DeviceName: a.DeviceName, Action: a.Action,
		Result: a.Result, Timestamp: a.Timestamp.UTC(), Message: a.Message,
	}
	if a.Result == model.ResultSuccess {
		status := a.Action.ResultingStatus()
		item.Status = &status
	}
	return item
}
