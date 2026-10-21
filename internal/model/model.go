// Package model holds the five domain entities of the report: User, Device, Sensor, SensorData
// and DeviceAction.
package model

import "time"

// DeviceID is the id of the first LED device (LED 1); LED 2 and 3 have ids 2 and 3.
const DeviceID int64 = 1

type Role string

const (
	RoleAdmin Role = "ADMIN"
	RoleUser  Role = "USER"
)

type User struct {
	ID           int64
	Name         *string
	Email        string
	PasswordHash string
	Username     string
	Phone        *string
	Avatar       *string
	Github       *string
	Figma        *string
	Swagger      *string
	Report       *string
	Role         Role
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type DeviceStatus string

const (
	DeviceOn  DeviceStatus = "ON"
	DeviceOff DeviceStatus = "OFF"
)

type Device struct {
	ID        int64
	Name      string
	Type      string
	Status    DeviceStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

type SensorType string

const (
	SensorTemperature SensorType = "temperature"
	SensorHumidity    SensorType = "humidity"
	SensorLight       SensorType = "light"
	SensorWindSpeed   SensorType = "wind_speed"
)

// SensorTypes lists the sensors in display order.
var SensorTypes = []SensorType{SensorTemperature, SensorHumidity, SensorLight, SensorWindSpeed}

func (t SensorType) Valid() bool {
	return t == SensorTemperature || t == SensorHumidity || t == SensorLight || t == SensorWindSpeed
}

// WindWarningThreshold is the wind speed (km/h) from which the warning LED lights up and a
// reading counts as a warning.
const WindWarningThreshold = 300.0

// WindLEDDeviceID is the warning LED ("LED WIND"): it follows the wind speed and is never
// switched by a user. DeviceTypeWarningLed is its device type.
const (
	WindLEDDeviceID      int64 = 3
	DeviceTypeWarningLed       = "Warning Led"
)

type Sensor struct {
	ID        int64
	Name      string
	Type      SensorType
	Unit      string
	Status    *string
	MQTTTopic *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type SensorData struct {
	ID        int64
	SensorID  int64
	Value     float64
	Timestamp time.Time
}

// SensorReading is a SensorData row joined with its sensor's type and unit.
type SensorReading struct {
	SensorData
	Type SensorType
	Unit string
}

type DeviceCommand string

const (
	CommandOn  DeviceCommand = "ON"
	CommandOff DeviceCommand = "OFF"
)

func (c DeviceCommand) Valid() bool { return c == CommandOn || c == CommandOff }

// Action is the history action recorded for the command.
func (c DeviceCommand) Action() ActionType {
	if c == CommandOn {
		return ActionTurnOn
	}
	return ActionTurnOff
}

// ResultingStatus is the device status after the command succeeds.
func (c DeviceCommand) ResultingStatus() DeviceStatus {
	if c == CommandOn {
		return DeviceOn
	}
	return DeviceOff
}

type ActionType string

const (
	ActionTurnOn  ActionType = "TURN_ON"
	ActionTurnOff ActionType = "TURN_OFF"
)

// ResultingStatus is the device status after the action succeeds.
func (a ActionType) ResultingStatus() DeviceStatus {
	if a == ActionTurnOn {
		return DeviceOn
	}
	return DeviceOff
}

type ActionResult string

const (
	ResultPending ActionResult = "PENDING"
	ResultSuccess ActionResult = "SUCCESS"
	ResultFailed  ActionResult = "FAILED"
	ResultTimeout ActionResult = "TIMEOUT"
)

type DeviceAction struct {
	ID          int64
	UserID      *int64
	DeviceID    int64
	Action      ActionType
	Result      ActionResult
	Message     *string
	Timestamp   time.Time
	CompletedAt *time.Time
}

// DeviceActionHistory is a DeviceAction joined with its device's name and the user who sent the
// command. User carries only ID, Username, Name and Role, and is nil when the user has since
// been deleted.
type DeviceActionHistory struct {
	DeviceAction
	DeviceName string
	User       *User
}
