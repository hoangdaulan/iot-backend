package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"iot-backend/internal/apperr"
	"iot-backend/internal/model"
	"iot-backend/internal/repository"
)

const (
	DefaultPageSize = 20
	MaxPageSize     = 5000
)

type SensorService struct {
	sensors repository.SensorRepository
	devices repository.DeviceRepository

	mu          sync.Mutex
	sensorByTyp map[model.SensorType]model.Sensor
}

func NewSensorService(sensors repository.SensorRepository, devices repository.DeviceRepository) *SensorService {
	return &SensorService{sensors: sensors, devices: devices}
}

func (s *SensorService) Sensors(ctx context.Context) ([]model.Sensor, error) {
	return s.sensors.List(ctx)
}

// Latest returns the newest reading per sensor and the current state of every device. The
// values are the latest ones persisted from MQTT; the firmware has no on-demand read request.
func (s *SensorService) Latest(ctx context.Context) ([]model.SensorReading, []model.Device, error) {
	readings, err := s.sensors.Latest(ctx)
	if err != nil {
		return nil, nil, err
	}
	devices, err := s.devices.List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("load devices: %w", err)
	}
	return readings, devices, nil
}

type HistoryQuery struct {
	Type     *model.SensorType
	From, To *time.Time
	Value    *float64
	Page     int
	Size     int
}

// Validate checks paging and the time range, applying the default page size.
func (q *HistoryQuery) Validate() error {
	if q.Size == 0 {
		q.Size = DefaultPageSize
	}
	if q.Page < 0 || q.Size < 1 || q.Size > MaxPageSize {
		return apperr.BadRequest(fmt.Sprintf("Invalid query parameters: page must be >= 0 and size 1-%d", MaxPageSize))
	}
	if q.From != nil && q.To != nil && q.From.After(*q.To) {
		return apperr.BadRequest("Invalid query parameters: time range start is after its end")
	}
	return nil
}

func (s *SensorService) History(ctx context.Context, q HistoryQuery) ([]model.SensorReading, int64, error) {
	if err := q.Validate(); err != nil {
		return nil, 0, err
	}
	if q.Type != nil && !q.Type.Valid() {
		return nil, 0, apperr.BadRequest("Invalid query parameters: type must be temperature, humidity or light")
	}
	return s.sensors.History(ctx, repository.SensorHistoryFilter{
		Type: q.Type, From: q.From, To: q.To, Value: q.Value,
		Offset: q.Page * q.Size, Limit: q.Size,
	})
}

// Ingest stores one SensorData row per value, using the three seeded sensors. Values for types
// without a sensor row are dropped; sensors are never created from MQTT.
func (s *SensorService) Ingest(ctx context.Context, values map[model.SensorType]float64, at time.Time) (int, error) {
	sensorByType, err := s.sensorsByType(ctx)
	if err != nil {
		return 0, err
	}
	rows := make([]model.SensorData, 0, len(values))
	for _, typ := range model.SensorTypes {
		value, ok := values[typ]
		sensor, known := sensorByType[typ]
		if ok && known {
			rows = append(rows, model.SensorData{SensorID: sensor.ID, Value: value, Timestamp: at})
		}
	}
	if err := s.sensors.InsertData(ctx, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func (s *SensorService) sensorsByType(ctx context.Context) (map[model.SensorType]model.Sensor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sensorByTyp != nil {
		return s.sensorByTyp, nil
	}
	sensors, err := s.sensors.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load sensors: %w", err)
	}
	byType := make(map[model.SensorType]model.Sensor, len(sensors))
	for _, sensor := range sensors {
		byType[sensor.Type] = sensor
	}
	s.sensorByTyp = byType
	return byType, nil
}
