package service

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
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

// Search fields of the history filter. FilterAll applies no search.
const (
	FilterAll    = "all"
	FilterSensor = "sensor"
	FilterTime   = "time"
	// A sensor type name (temperature, humidity, light) filters by type and searches the value.
)

type HistoryQuery struct {
	Type     *model.SensorType
	From, To *time.Time
	Value    *float64
	// Bucket, when set, averages each sensor's readings over windows of this length (a whole
	// number of minutes, 1 minute to 24 hours).
	Bucket time.Duration
	// Filter selects what Query searches: FilterAll, FilterSensor (sensor id or name), a sensor
	// type (measured value) or FilterTime (yyyy/MM/dd HH:mm:ss prefix). Empty means FilterAll.
	Filter string
	Query  string
	// UtcOffsetMinutes is the client's offset east of UTC, used to read a FilterTime query.
	UtcOffsetMinutes int
	Page             int
	Size             int
}

// Validate checks paging and the time range, applying the default page size.
func (q *HistoryQuery) Validate() error {
	if q.Size == 0 {
		q.Size = DefaultPageSize
	}
	if q.Page < 0 || q.Size < 1 || q.Size > MaxPageSize {
		return apperr.BadRequest(fmt.Sprintf("Invalid query parameters: page must be >= 0 and size 1-%d", MaxPageSize))
	}
	if q.Bucket != 0 && (q.Bucket < time.Minute || q.Bucket > 24*time.Hour || q.Bucket%time.Minute != 0) {
		return apperr.BadRequest("Invalid query parameters: bucket must be a whole number of minutes from 1m to 24h")
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
	filter := repository.SensorHistoryFilter{
		Type: q.Type, From: q.From, To: q.To, Value: q.Value, Bucket: q.Bucket,
		Offset: q.Page * q.Size, Limit: q.Size,
	}
	if err := q.applySearch(&filter); err != nil {
		return nil, 0, err
	}
	return s.sensors.History(ctx, filter)
}

// applySearch narrows f by the Filter/Query pair. FilterAll with a query keeps what matches any
// of the other filters: the sensor, the value or the time.
func (q *HistoryQuery) applySearch(f *repository.SensorHistoryFilter) error {
	query := strings.TrimSpace(q.Query)
	switch q.Filter {
	case "", FilterAll:
		if query != "" {
			f.Any = q.anyOf(query)
		}
		return nil
	case FilterSensor:
		if query == "" {
			return nil
		}
		f.SensorName = query
		f.SensorID = parseID(query)
		return nil
	case FilterTime:
		if query == "" {
			return nil
		}
		from, to, err := parseTimePrefix(query, q.UtcOffsetMinutes)
		if err != nil {
			return err
		}
		f.From, f.To = laterOf(f.From, from), earlierOf(f.To, to)
		return nil
	}

	t := model.SensorType(q.Filter)
	if !t.Valid() {
		return apperr.BadRequest("Invalid query parameters: filter must be all, sensor, temperature, humidity, light or time")
	}
	f.Type = &t
	if query == "" {
		return nil
	}
	from, to, err := valueRange(query)
	if err != nil {
		return err
	}
	f.ValueFrom, f.ValueTo = &from, &to
	return nil
}

// anyOf builds the union for FilterAll. A query that is not a number or a time simply adds no
// condition for it instead of being rejected.
func (q *HistoryQuery) anyOf(query string) *repository.SensorAnyOf {
	any := &repository.SensorAnyOf{SensorName: query, SensorID: parseID(query)}
	if from, to, err := valueRange(query); err == nil {
		any.ValueFrom, any.ValueTo = &from, &to
	}
	if from, to, err := parseTimePrefix(query, q.UtcOffsetMinutes); err == nil {
		any.From, any.To = from, to
	}
	return any
}

func parseID(query string) *int64 {
	id, err := strconv.ParseInt(query, 10, 64)
	if err != nil {
		return nil
	}
	return &id
}

// valueRange returns the values that start with the typed number: "28" is every value with
// integer part 28 (28 up to, not including, 29) and "28.5" every value from 28.5 up to 28.6.
// A negative number counts its digits the same way, so "-3" is -3.99 up to -3.
func valueRange(query string) (from, to float64, err error) {
	v, err := strconv.ParseFloat(query, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, 0, apperr.BadRequest("Invalid query parameters: q must be a number")
	}
	decimals := 0
	if _, frac, ok := strings.Cut(query, "."); ok {
		decimals = len(frac)
	}
	step := 1 / math.Pow10(decimals)
	// Round-trip through text so a bound is the same double as the stored value it names.
	round := func(x float64) float64 {
		r, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', decimals, 64), 64)
		return r
	}
	if strings.HasPrefix(query, "-") {
		// (v-step, v]: the lower bound is exclusive and the upper one inclusive.
		return math.Nextafter(round(v-step), math.Inf(1)), math.Nextafter(v, math.Inf(1)), nil
	}
	return v, round(v + step), nil
}

var timePrefixPattern = regexp.MustCompile(
	`^(\d{4})(?:[/-](\d{1,2})(?:[/-](\d{1,2})(?:[ T](\d{1,2})(?::(\d{1,2})(?::(\d{1,2}))?)?)?)?)?$`)

// parseTimePrefix reads yyyy[/MM[/dd[ HH[:mm[:ss]]]]] as the whole period it names, in the
// client's time zone: "2026/10/06 11" is 11:00:00 through 11:59:59 on 6 October 2026.
func parseTimePrefix(query string, utcOffsetMinutes int) (from, to *time.Time, err error) {
	invalid := apperr.BadRequest("Invalid query parameters: q must be yyyy/MM/dd HH:mm:ss, or a leading part of it")
	m := timePrefixPattern.FindStringSubmatch(query)
	if m == nil {
		return nil, nil, invalid
	}
	// parts: year, month, day, hour, minute, second; -1 marks a part that was not typed.
	var parts [6]int
	given := 0
	for i := range parts {
		parts[i] = -1
		if m[i+1] != "" {
			parts[i], _ = strconv.Atoi(m[i+1])
			given = i + 1
		}
	}
	loc := time.FixedZone("client", utcOffsetMinutes*60)
	start := time.Date(parts[0], time.Month(max(parts[1], 1)), max(parts[2], 1),
		max(parts[3], 0), max(parts[4], 0), max(parts[5], 0), 0, loc)
	// Date normalizes out-of-range parts (month 13, day 32...), which must be rejected instead.
	got := [6]int{start.Year(), int(start.Month()), start.Day(), start.Hour(), start.Minute(), start.Second()}
	for i := range given {
		if got[i] != parts[i] {
			return nil, nil, invalid
		}
	}
	var end time.Time
	switch given {
	case 1:
		end = start.AddDate(1, 0, 0)
	case 2:
		end = start.AddDate(0, 1, 0)
	case 3:
		end = start.AddDate(0, 0, 1)
	case 4:
		end = start.Add(time.Hour)
	case 5:
		end = start.Add(time.Minute)
	default:
		end = start.Add(time.Second)
	}
	start, end = start.UTC(), end.Add(-time.Microsecond).UTC()
	return &start, &end, nil
}

// laterOf and earlierOf combine an explicit bound with a searched one; nil means unbounded.
func laterOf(a, b *time.Time) *time.Time {
	if a == nil || (b != nil && b.After(*a)) {
		return b
	}
	return a
}

func earlierOf(a, b *time.Time) *time.Time {
	if a == nil || (b != nil && b.Before(*a)) {
		return b
	}
	return a
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
