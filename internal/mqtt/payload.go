package mqtt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"iot-backend/internal/model"
	"iot-backend/internal/service"
)

// SensorPayload is the ESP32 message on the sensor-data topic:
//
//	{"temperature": 28.7, "humidity": 60.5, "light": 420, "timestamp": "2026-10-05T08:00:00Z"}
//
// "lux" is accepted as an alias of "light". Every field is optional; "timestamp" defaults to the time the message is received.
type SensorPayload struct {
	Values    map[model.SensorType]float64
	Timestamp *time.Time
	// Rejected lists values that were present but outside the sensor's physical range.
	Rejected []string
}

// Valid ranges of the documented hardware (DHT11/DHT22, BH1750).
var sensorRanges = map[model.SensorType][2]float64{
	model.SensorTemperature: {-40, 125},
	model.SensorHumidity:    {0, 100},
	model.SensorLight:       {0, 65535},
}

// ParseSensorPayload decodes and validates a sensor-data message. It fails if the message is not
// a JSON object or contains no valid value.
func ParseSensorPayload(raw []byte) (SensorPayload, error) {
	var msg struct {
		Temperature *float64 `json:"temperature"`
		Humidity    *float64 `json:"humidity"`
		Light       *float64 `json:"light"`
		Lux         *float64 `json:"lux"`
		Timestamp   *string  `json:"timestamp"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &msg); err != nil {
		return SensorPayload{}, fmt.Errorf("invalid sensor payload: %w", err)
	}

	if msg.Light == nil {
		msg.Light = msg.Lux
	}

	p := SensorPayload{Values: map[model.SensorType]float64{}}
	for typ, value := range map[model.SensorType]*float64{
		model.SensorTemperature: msg.Temperature,
		model.SensorHumidity:    msg.Humidity,
		model.SensorLight:       msg.Light,
	} {
		if value == nil {
			continue
		}
		bounds := sensorRanges[typ]
		if math.IsNaN(*value) || *value < bounds[0] || *value > bounds[1] {
			p.Rejected = append(p.Rejected, fmt.Sprintf("%s=%v", typ, *value))
			continue
		}
		p.Values[typ] = *value
	}
	if msg.Timestamp != nil {
		if t, err := time.Parse(time.RFC3339Nano, *msg.Timestamp); err == nil {
			t = t.UTC()
			p.Timestamp = &t
		} else {
			p.Rejected = append(p.Rejected, "timestamp="+*msg.Timestamp)
		}
	}
	if len(p.Values) == 0 {
		return p, errors.New("sensor payload has no valid temperature, humidity or light value")
	}
	return p, nil
}

// ParseDeviceResponse decodes an ESP32 message on the device-response topic:
//
//	{"actionId": 42, "status": "SUCCESS", "state": "ON", "message": "LED on"}
//
// "status" is SUCCESS or FAILED; "actionId", "state" and "message" are optional.
func ParseDeviceResponse(raw []byte) (service.DeviceResponse, error) {
	var msg struct {
		ActionID int64   `json:"actionId"`
		Status   string  `json:"status"`
		State    *string `json:"state"`
		Message  string  `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &msg); err != nil {
		return service.DeviceResponse{}, fmt.Errorf("invalid device response: %w", err)
	}
	resp := service.DeviceResponse{ActionID: msg.ActionID, Message: msg.Message}
	switch model.ActionResult(msg.Status) {
	case model.ResultSuccess, model.ResultFailed:
		resp.Status = model.ActionResult(msg.Status)
	default:
		return service.DeviceResponse{}, fmt.Errorf("invalid device response status %q", msg.Status)
	}
	if msg.State != nil {
		switch state := model.DeviceStatus(*msg.State); state {
		case model.DeviceOn, model.DeviceOff:
			resp.State = &state
		default:
			return service.DeviceResponse{}, fmt.Errorf("invalid device state %q", *msg.State)
		}
	}
	return resp, nil
}
