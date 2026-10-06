// Package repository defines the persistence interfaces used by the services. The postgres
// package implements them for production, the memory package for tests.
package repository

import (
	"context"
	"errors"
	"time"

	"iot-backend/internal/model"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict reports a unique constraint violation (duplicate username or email).
	ErrConflict = errors.New("conflict")
)

type UserRepository interface {
	// Create inserts the user and sets its ID and timestamps.
	Create(ctx context.Context, u *model.User) error
	// FindByLogin finds a user by username or email.
	FindByLogin(ctx context.Context, login string) (*model.User, error)
	FindByID(ctx context.Context, id int64) (*model.User, error)
	UpdateProfile(ctx context.Context, id int64, p ProfileUpdate) (*model.User, error)
	UpdatePassword(ctx context.Context, id int64, passwordHash string) error
}

// ProfileUpdate changes only its non-nil fields.
type ProfileUpdate struct {
	Name, Phone, Avatar, Github, Figma, Swagger *string
}

type SensorRepository interface {
	List(ctx context.Context) ([]model.Sensor, error)
	InsertData(ctx context.Context, rows []model.SensorData) error
	// Latest returns the newest reading of each sensor that has data.
	Latest(ctx context.Context) ([]model.SensorReading, error)
	// History returns one page of readings, newest first, and the total number of matches.
	History(ctx context.Context, f SensorHistoryFilter) ([]model.SensorReading, int64, error)
}

type SensorHistoryFilter struct {
	Type     *model.SensorType
	From, To *time.Time
	Value    *float64
	// ValueFrom (inclusive) and ValueTo (exclusive) bound the measured value.
	ValueFrom, ValueTo *float64
	// SensorName is a case-insensitive substring of the sensor name; with SensorID set, a
	// sensor also matches by that exact id.
	SensorName string
	SensorID   *int64
	// Any, when set, keeps readings matching at least one of its conditions.
	Any           *SensorAnyOf
	Offset, Limit int
}

// SensorAnyOf is a union of conditions; the ones left empty are not part of it. A reading
// matches by its sensor (name substring or id), its value in [ValueFrom, ValueTo), or its
// timestamp in [From, To].
type SensorAnyOf struct {
	SensorName         string
	SensorID           *int64
	ValueFrom, ValueTo *float64
	From, To           *time.Time
}

type DeviceRepository interface {
	FindByID(ctx context.Context, id int64) (*model.Device, error)
	// List returns all devices ordered by id.
	List(ctx context.Context) ([]model.Device, error)
	UpdateStatus(ctx context.Context, id int64, status model.DeviceStatus) error
	// CreateAction inserts the action and sets its ID and timestamp.
	CreateAction(ctx context.Context, a *model.DeviceAction) error
	CompleteAction(ctx context.Context, id int64, result model.ActionResult, message string) error
	// ActionHistory returns one page of actions, newest first, and the total number of matches.
	ActionHistory(ctx context.Context, f ActionHistoryFilter) ([]model.DeviceActionHistory, int64, error)
}

type ActionHistoryFilter struct {
	DeviceID      *int64
	Action        *model.ActionType
	Result        *model.ActionResult
	From, To      *time.Time
	Offset, Limit int
}
