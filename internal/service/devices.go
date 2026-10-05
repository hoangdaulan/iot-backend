package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"iot-backend/internal/apperr"
	"iot-backend/internal/model"
	"iot-backend/internal/repository"
)

// Publisher sends a control message to the ESP32.
type Publisher interface {
	PublishControl(ctx context.Context, payload []byte) error
}

// ControlMessage is published on the device-control topic.
type ControlMessage struct {
	ActionID int64               `json:"actionId"`
	DeviceID int64               `json:"deviceId"`
	Command  model.DeviceCommand `json:"command"`
}

// DeviceResponse is what the ESP32 reports on the device-response topic.
type DeviceResponse struct {
	// ActionID echoes ControlMessage.ActionID; 0 means "the command in flight".
	ActionID int64
	Status   model.ActionResult // SUCCESS or FAILED
	State    *model.DeviceStatus
	Message  string
}

// CommandResult is the outcome returned to the client.
type CommandResult struct {
	DeviceID int64
	Command  model.DeviceCommand
	Status   model.ActionResult
	Message  string
}

type pendingCommand struct {
	actionID int64
	response chan DeviceResponse
}

// DeviceService sends LED commands and serves the control history. Commands are serialized:
// one command waits for the ESP32 at a time.
type DeviceService struct {
	devices   repository.DeviceRepository
	publisher Publisher
	timeout   time.Duration
	logger    *slog.Logger

	commandMu sync.Mutex

	pendingMu sync.Mutex
	pending   *pendingCommand
}

func NewDeviceService(
	devices repository.DeviceRepository, publisher Publisher, timeout time.Duration, logger *slog.Logger,
) *DeviceService {
	return &DeviceService{devices: devices, publisher: publisher, timeout: timeout, logger: logger}
}

// SendCommand records a PENDING action, publishes it over MQTT and waits for the ESP32. The
// result is SUCCESS or FAILED as reported by the device, or TIMEOUT.
func (s *DeviceService) SendCommand(
	ctx context.Context, userID, deviceID int64, command model.DeviceCommand,
) (*CommandResult, error) {
	if !command.Valid() {
		return nil, apperr.BadRequest("Invalid command")
	}
	if _, err := s.devices.FindByID(ctx, deviceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("Device not found")
		}
		return nil, err
	}

	s.commandMu.Lock()
	defer s.commandMu.Unlock()

	action := &model.DeviceAction{
		UserID: &userID, DeviceID: deviceID, Action: command.Action(), Result: model.ResultPending,
	}
	if err := s.devices.CreateAction(ctx, action); err != nil {
		return nil, err
	}

	waiting := &pendingCommand{actionID: action.ID, response: make(chan DeviceResponse, 1)}
	s.setPending(waiting)
	defer s.setPending(nil)

	result := &CommandResult{DeviceID: deviceID, Command: command}
	payload, _ := json.Marshal(ControlMessage{ActionID: action.ID, DeviceID: deviceID, Command: command})
	if err := s.publisher.PublishControl(ctx, payload); err != nil {
		s.logger.Error("publish device command", "actionId", action.ID, "error", err)
		result.Status, result.Message = model.ResultFailed, "Could not reach the MQTT broker"
	} else {
		timer := time.NewTimer(s.timeout)
		defer timer.Stop()
		select {
		case resp := <-waiting.response:
			result.Status, result.Message = resp.Status, resp.Message
			if resp.Status == model.ResultSuccess {
				status := command.ResultingStatus()
				if resp.State != nil {
					status = *resp.State
				}
				// Persist even if the client has gone away; the LED did switch.
				if err := s.devices.UpdateStatus(context.WithoutCancel(ctx), deviceID, status); err != nil {
					return nil, err
				}
			}
		case <-timer.C:
			result.Status = model.ResultTimeout
		case <-ctx.Done():
			result.Status = model.ResultTimeout
		}
	}

	if result.Message == "" {
		result.Message = defaultMessage(command, result.Status)
	}
	if err := s.devices.CompleteAction(context.WithoutCancel(ctx), action.ID, result.Status, result.Message); err != nil {
		return nil, err
	}
	return result, nil
}

// HandleResponse delivers an ESP32 response to the command waiting for it. Responses for other
// or already finished commands are ignored.
func (s *DeviceService) HandleResponse(resp DeviceResponse) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.pending == nil {
		s.logger.Warn("device response without a pending command", "actionId", resp.ActionID)
		return
	}
	if resp.ActionID != 0 && resp.ActionID != s.pending.actionID {
		s.logger.Warn("device response for another command",
			"actionId", resp.ActionID, "pendingActionId", s.pending.actionID)
		return
	}
	select {
	case s.pending.response <- resp:
	default: // already answered
	}
}

func (s *DeviceService) setPending(p *pendingCommand) {
	s.pendingMu.Lock()
	s.pending = p
	s.pendingMu.Unlock()
}

type ActionHistoryQuery struct {
	DeviceID *int64
	From, To *time.Time
	Page     int
	Size     int
}

func (s *DeviceService) History(
	ctx context.Context, q ActionHistoryQuery,
) ([]model.DeviceActionHistory, int64, error) {
	paging := HistoryQuery{From: q.From, To: q.To, Page: q.Page, Size: q.Size}
	if err := paging.Validate(); err != nil {
		return nil, 0, err
	}
	return s.devices.ActionHistory(ctx, repository.ActionHistoryFilter{
		DeviceID: q.DeviceID, From: q.From, To: q.To,
		Offset: paging.Page * paging.Size, Limit: paging.Size,
	})
}

func defaultMessage(command model.DeviceCommand, status model.ActionResult) string {
	switch status {
	case model.ResultSuccess:
		if command == model.CommandOn {
			return "Device turned on successfully"
		}
		return "Device turned off successfully"
	case model.ResultTimeout:
		return "Device did not respond in time"
	default:
		return fmt.Sprintf("Device failed to turn %s", map[model.DeviceCommand]string{
			model.CommandOn: "on", model.CommandOff: "off",
		}[command])
	}
}
