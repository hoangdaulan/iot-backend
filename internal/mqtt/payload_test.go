package mqtt

import (
	"testing"
	"time"

	"iot-backend/internal/model"
)

func TestParseSensorPayload(t *testing.T) {
	t.Run("all three values", func(t *testing.T) {
		p, err := ParseSensorPayload([]byte(`{"temperature": 28.7, "humidity": 60.5, "light": 420}`))
		if err != nil {
			t.Fatal(err)
		}
		want := map[model.SensorType]float64{"temperature": 28.7, "humidity": 60.5, "light": 420}
		if len(p.Values) != 3 {
			t.Fatalf("values = %v", p.Values)
		}
		for typ, v := range want {
			if p.Values[typ] != v {
				t.Errorf("%s = %v, want %v", typ, p.Values[typ], v)
			}
		}
		if p.Timestamp != nil || len(p.Rejected) != 0 {
			t.Errorf("timestamp = %v, rejected = %v", p.Timestamp, p.Rejected)
		}
	})

	t.Run("partial payload with timestamp", func(t *testing.T) {
		p, err := ParseSensorPayload([]byte(`{"light": 0, "timestamp": "2026-10-05T08:00:00+07:00"}`))
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := p.Values[model.SensorLight]; !ok || v != 0 || len(p.Values) != 1 {
			t.Errorf("values = %v", p.Values)
		}
		if want := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC); p.Timestamp == nil || !p.Timestamp.Equal(want) {
			t.Errorf("timestamp = %v, want %v", p.Timestamp, want)
		}
	})

	t.Run("out-of-range values and a bad timestamp are dropped individually", func(t *testing.T) {
		p, err := ParseSensorPayload([]byte(
			`{"temperature": 500, "humidity": 55, "light": -3, "timestamp": "yesterday"}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Values) != 1 || p.Values[model.SensorHumidity] != 55 {
			t.Errorf("values = %v", p.Values)
		}
		if p.Timestamp != nil || len(p.Rejected) != 3 {
			t.Errorf("timestamp = %v, rejected = %v", p.Timestamp, p.Rejected)
		}
	})

	for name, raw := range map[string]string{
		"not json":         `temperature=28`,
		"wrong value type": `{"temperature": "hot"}`,
		"no known fields":  `{"pressure": 1013}`,
		"all invalid":      `{"humidity": 120}`,
		"json array":       `[28.7, 60.5, 420]`,
		"empty":            ``,
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := ParseSensorPayload([]byte(raw)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestParseDeviceResponse(t *testing.T) {
	resp, err := ParseDeviceResponse([]byte(`{"actionId": 42, "status": "SUCCESS", "state": "ON", "message": "LED on"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ActionID != 42 || resp.Status != model.ResultSuccess || resp.State == nil ||
		*resp.State != model.DeviceOn || resp.Message != "LED on" {
		t.Errorf("resp = %+v", resp)
	}

	resp, err = ParseDeviceResponse([]byte(`{"status": "FAILED"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ActionID != 0 || resp.Status != model.ResultFailed || resp.State != nil {
		t.Errorf("resp = %+v", resp)
	}

	for _, raw := range []string{`{"status": "TIMEOUT"}`, `{"status": "ok"}`, `{}`,
		`{"status": "SUCCESS", "state": "BLINK"}`, `nope`} {
		if _, err := ParseDeviceResponse([]byte(raw)); err == nil {
			t.Errorf("%s: expected an error", raw)
		}
	}
}
