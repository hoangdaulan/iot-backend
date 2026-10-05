package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"iot-backend/internal/apperr"
	"iot-backend/internal/model"
	"iot-backend/internal/repository"
	"iot-backend/internal/repository/memory"
	"iot-backend/internal/service"
)

// fakeESP32 receives published commands and answers them the way the test configures.
type fakeESP32 struct {
	mu         sync.Mutex
	devices    *service.DeviceService
	respond    func(service.ControlMessage) *service.DeviceResponse
	publishErr error
	received   []service.ControlMessage
}

func (f *fakeESP32) PublishControl(_ context.Context, payload []byte) error {
	if f.publishErr != nil {
		return f.publishErr
	}
	var msg service.ControlMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}
	f.mu.Lock()
	f.received = append(f.received, msg)
	f.mu.Unlock()
	if resp := f.respond(msg); resp != nil {
		go f.devices.HandleResponse(*resp)
	}
	return nil
}

func newDeviceService(t *testing.T, esp *fakeESP32, timeout time.Duration) (*service.DeviceService, *memory.Store) {
	t.Helper()
	store := memory.NewStore()
	svc := service.NewDeviceService(store.Devices(), esp, timeout, slog.New(slog.NewTextHandler(io.Discard, nil)))
	esp.devices = svc
	return svc, store
}

func history(t *testing.T, store *memory.Store) []model.DeviceActionHistory {
	t.Helper()
	items, _, err := store.Devices().ActionHistory(context.Background(), repository.ActionHistoryFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func ledStatus(t *testing.T, store *memory.Store) model.DeviceStatus {
	t.Helper()
	d, err := store.Devices().FindByID(context.Background(), model.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	return d.Status
}

func TestSendCommandSuccess(t *testing.T) {
	esp := &fakeESP32{respond: func(m service.ControlMessage) *service.DeviceResponse {
		return &service.DeviceResponse{ActionID: m.ActionID, Status: model.ResultSuccess}
	}}
	svc, store := newDeviceService(t, esp, time.Second)

	result, err := svc.SendCommand(context.Background(), 1, model.DeviceID, model.CommandOn)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.ResultSuccess || result.Message != "Device turned on successfully" {
		t.Errorf("result = %+v", result)
	}
	if got := ledStatus(t, store); got != model.DeviceOn {
		t.Errorf("LED status = %s, want ON", got)
	}
	actions := history(t, store)
	if len(actions) != 1 || actions[0].Action != model.ActionTurnOn || actions[0].Result != model.ResultSuccess ||
		*actions[0].UserID != 1 || actions[0].CompletedAt == nil {
		t.Errorf("history = %+v", actions)
	}
	if len(esp.received) != 1 || esp.received[0] != (service.ControlMessage{ActionID: actions[0].ID, Command: model.CommandOn}) {
		t.Errorf("published = %+v", esp.received)
	}
}

func TestSendCommandFailedKeepsDeviceStatus(t *testing.T) {
	esp := &fakeESP32{respond: func(m service.ControlMessage) *service.DeviceResponse {
		return &service.DeviceResponse{ActionID: m.ActionID, Status: model.ResultFailed, Message: "LED driver fault"}
	}}
	svc, store := newDeviceService(t, esp, time.Second)

	result, err := svc.SendCommand(context.Background(), 1, model.DeviceID, model.CommandOn)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.ResultFailed || result.Message != "LED driver fault" {
		t.Errorf("result = %+v", result)
	}
	if got := ledStatus(t, store); got != model.DeviceOff {
		t.Errorf("LED status = %s, want OFF", got)
	}
	if a := history(t, store)[0]; a.Result != model.ResultFailed || *a.Message != "LED driver fault" {
		t.Errorf("action = %+v", a)
	}
}

func TestSendCommandTimeout(t *testing.T) {
	esp := &fakeESP32{respond: func(service.ControlMessage) *service.DeviceResponse { return nil }}
	svc, store := newDeviceService(t, esp, 50*time.Millisecond)

	result, err := svc.SendCommand(context.Background(), 1, model.DeviceID, model.CommandOff)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.ResultTimeout || result.Message != "Device did not respond in time" {
		t.Errorf("result = %+v", result)
	}
	if a := history(t, store)[0]; a.Result != model.ResultTimeout || a.Action != model.ActionTurnOff {
		t.Errorf("action = %+v", a)
	}

	// A late response after the timeout must not affect anything.
	svc.HandleResponse(service.DeviceResponse{ActionID: esp.received[0].ActionID, Status: model.ResultSuccess})
	if got := ledStatus(t, store); got != model.DeviceOff {
		t.Errorf("LED status = %s, want OFF", got)
	}
}

func TestSendCommandUsesReportedState(t *testing.T) {
	on := model.DeviceOn
	esp := &fakeESP32{respond: func(m service.ControlMessage) *service.DeviceResponse {
		// actionId omitted: matched to the command in flight.
		return &service.DeviceResponse{Status: model.ResultSuccess, State: &on}
	}}
	svc, store := newDeviceService(t, esp, time.Second)

	if _, err := svc.SendCommand(context.Background(), 1, model.DeviceID, model.CommandOff); err != nil {
		t.Fatal(err)
	}
	if got := ledStatus(t, store); got != model.DeviceOn {
		t.Errorf("LED status = %s, want the reported ON", got)
	}
}

func TestSendCommandIgnoresResponsesForOtherActions(t *testing.T) {
	esp := &fakeESP32{respond: func(m service.ControlMessage) *service.DeviceResponse {
		return &service.DeviceResponse{ActionID: m.ActionID + 99, Status: model.ResultSuccess}
	}}
	svc, _ := newDeviceService(t, esp, 50*time.Millisecond)

	result, err := svc.SendCommand(context.Background(), 1, model.DeviceID, model.CommandOn)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.ResultTimeout {
		t.Errorf("status = %s, want TIMEOUT", result.Status)
	}
}

func TestSendCommandBrokerUnavailable(t *testing.T) {
	esp := &fakeESP32{publishErr: errors.New("not connected")}
	svc, store := newDeviceService(t, esp, time.Second)

	result, err := svc.SendCommand(context.Background(), 1, model.DeviceID, model.CommandOn)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.ResultFailed || result.Message != "Could not reach the MQTT broker" {
		t.Errorf("result = %+v", result)
	}
	if a := history(t, store)[0]; a.Result != model.ResultFailed {
		t.Errorf("action = %+v", a)
	}
}

func TestSendCommandValidation(t *testing.T) {
	svc, store := newDeviceService(t, &fakeESP32{}, time.Second)

	_, err := svc.SendCommand(context.Background(), 1, 2, model.CommandOn)
	if appErr, ok := apperr.As(err); !ok || appErr.Status != 404 || appErr.Message != "Device not found" {
		t.Errorf("unknown device: %v", err)
	}
	_, err = svc.SendCommand(context.Background(), 1, model.DeviceID, "BLINK")
	if appErr, ok := apperr.As(err); !ok || appErr.Status != 400 {
		t.Errorf("invalid command: %v", err)
	}
	if len(history(t, store)) != 0 {
		t.Error("rejected commands must not be recorded")
	}
}

func TestSendCommandSerializesConcurrentCommands(t *testing.T) {
	esp := &fakeESP32{respond: func(m service.ControlMessage) *service.DeviceResponse {
		return &service.DeviceResponse{ActionID: m.ActionID, Status: model.ResultSuccess}
	}}
	svc, store := newDeviceService(t, esp, time.Second)

	var wg sync.WaitGroup
	for _, cmd := range []model.DeviceCommand{model.CommandOn, model.CommandOff, model.CommandOn, model.CommandOff} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := svc.SendCommand(context.Background(), 1, model.DeviceID, cmd); err != nil || r.Status != model.ResultSuccess {
				t.Errorf("command %s: %+v %v", cmd, r, err)
			}
		}()
	}
	wg.Wait()
	for _, a := range history(t, store) {
		if a.Result != model.ResultSuccess {
			t.Errorf("action %d result %s", a.ID, a.Result)
		}
	}
}
