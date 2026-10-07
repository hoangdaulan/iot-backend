// Package memory implements the repository interfaces in memory, mirroring the PostgreSQL
// behaviour (filters, ordering, paging, unique constraints). It is used by tests.
package memory

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"iot-backend/internal/model"
	"iot-backend/internal/repository"
)

// Store holds all data; its repositories share one lock.
type Store struct {
	mu        sync.Mutex
	users     []model.User
	devices   map[int64]*model.Device
	sensors   []model.Sensor
	data      []model.SensorData
	actions   []model.DeviceAction
	nextIDs   map[string]int64
	clockFunc func() time.Time
}

// NewStore returns a store seeded like the initial migration: devices 1-3 (LED 1-3, OFF) and the
// three sensors.
func NewStore() *Store {
	now := time.Now().UTC()
	return &Store{
		devices: map[int64]*model.Device{
			1: {ID: 1, Name: "LED 1", Type: "LED", Status: model.DeviceOff, CreatedAt: now, UpdatedAt: now},
			2: {ID: 2, Name: "LED 2", Type: "LED", Status: model.DeviceOff, CreatedAt: now, UpdatedAt: now},
			3: {ID: 3, Name: "LED 3", Type: "LED", Status: model.DeviceOff, CreatedAt: now, UpdatedAt: now},
		},
		sensors: []model.Sensor{
			{ID: 1, Name: "Temperature", Type: model.SensorTemperature, Unit: "°C", CreatedAt: now, UpdatedAt: now},
			{ID: 2, Name: "Humidity", Type: model.SensorHumidity, Unit: "%", CreatedAt: now, UpdatedAt: now},
			{ID: 3, Name: "Light", Type: model.SensorLight, Unit: "lux", CreatedAt: now, UpdatedAt: now},
		},
		nextIDs:   map[string]int64{},
		clockFunc: func() time.Time { return time.Now().UTC() },
	}
}

func (s *Store) nextID(table string) int64 {
	s.nextIDs[table]++
	return s.nextIDs[table]
}

func (s *Store) Users() *UserRepository     { return &UserRepository{s} }
func (s *Store) Sensors() *SensorRepository { return &SensorRepository{s} }
func (s *Store) Devices() *DeviceRepository { return &DeviceRepository{s} }

// ── Users ──

type UserRepository struct{ s *Store }

func (r *UserRepository) Create(_ context.Context, u *model.User) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, existing := range r.s.users {
		if existing.Username == u.Username || existing.Email == u.Email {
			return repository.ErrConflict
		}
	}
	u.ID = r.s.nextID("users")
	u.CreatedAt = r.s.clockFunc()
	u.UpdatedAt = u.CreatedAt
	r.s.users = append(r.s.users, *u)
	return nil
}

func (r *UserRepository) FindByLogin(_ context.Context, login string) (*model.User, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, u := range r.s.users {
		if u.Username == login || u.Email == strings.ToLower(login) {
			return &u, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *UserRepository) FindByID(_ context.Context, id int64) (*model.User, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, u := range r.s.users {
		if u.ID == id {
			return &u, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *UserRepository) UpdateProfile(
	_ context.Context, id int64, p repository.ProfileUpdate,
) (*model.User, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for i := range r.s.users {
		u := &r.s.users[i]
		if u.ID != id {
			continue
		}
		set := func(dst **string, v *string) {
			if v != nil {
				value := *v
				*dst = &value
			}
		}
		set(&u.Name, p.Name)
		set(&u.Phone, p.Phone)
		set(&u.Avatar, p.Avatar)
		set(&u.Github, p.Github)
		set(&u.Figma, p.Figma)
		set(&u.Swagger, p.Swagger)
		u.UpdatedAt = r.s.clockFunc()
		copied := *u
		return &copied, nil
	}
	return nil, repository.ErrNotFound
}

func (r *UserRepository) UpdatePassword(_ context.Context, id int64, hash string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for i := range r.s.users {
		if r.s.users[i].ID == id {
			r.s.users[i].PasswordHash = hash
			return nil
		}
	}
	return repository.ErrNotFound
}

// ── Sensors ──

type SensorRepository struct{ s *Store }

func (r *SensorRepository) List(context.Context) ([]model.Sensor, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return append([]model.Sensor(nil), r.s.sensors...), nil
}

func (r *SensorRepository) InsertData(_ context.Context, rows []model.SensorData) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, row := range rows {
		row.ID = r.s.nextID("sensor_data")
		r.s.data = append(r.s.data, row)
	}
	return nil
}

func (r *SensorRepository) reading(d model.SensorData) model.SensorReading {
	for _, s := range r.s.sensors {
		if s.ID == d.SensorID {
			return model.SensorReading{SensorData: d, Type: s.Type, Unit: s.Unit}
		}
	}
	return model.SensorReading{SensorData: d}
}

// sorted returns readings newest first, ties by sensor id then id descending (as in SQL).
func (r *SensorRepository) sorted(keep func(model.SensorReading) bool) []model.SensorReading {
	var out []model.SensorReading
	for _, d := range r.s.data {
		if reading := r.reading(d); keep(reading) {
			out = append(out, reading)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.After(out[j].Timestamp)
		}
		if out[i].SensorID != out[j].SensorID {
			return out[i].SensorID < out[j].SensorID
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func (r *SensorRepository) Latest(context.Context) ([]model.SensorReading, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []model.SensorReading
	for _, s := range r.s.sensors {
		all := r.sorted(func(rd model.SensorReading) bool { return rd.SensorID == s.ID })
		if len(all) > 0 {
			out = append(out, all[0])
		}
	}
	return out, nil
}

func (r *SensorRepository) History(
	_ context.Context, f repository.SensorHistoryFilter,
) ([]model.SensorReading, int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	all := r.sorted(func(rd model.SensorReading) bool {
		return (f.Type == nil || rd.Type == *f.Type) &&
			(f.From == nil || !rd.Timestamp.Before(*f.From)) &&
			(f.To == nil || !rd.Timestamp.After(*f.To)) &&
			(f.Value == nil || rd.Value == *f.Value) &&
			(f.ValueFrom == nil || rd.Value >= *f.ValueFrom) &&
			(f.ValueTo == nil || rd.Value < *f.ValueTo) &&
			r.matchesSensor(rd, f) &&
			r.matchesAny(rd, f.Any)
	})
	if f.Bucket > 0 {
		all = bucketReadings(all, f.Bucket)
	}
	return page(all, f.Offset, f.Limit), int64(len(all)), nil
}

// bucketReadings averages newest-first readings per sensor over windows of d, like the SQL
// version: the id is the smallest of the window and the timestamp its start.
func bucketReadings(all []model.SensorReading, d time.Duration) []model.SensorReading {
	type key struct {
		sensor int64
		start  int64
	}
	groups := map[key]*model.SensorReading{}
	counts := map[key]int{}
	sums := map[key]float64{}
	var order []key
	for _, r := range all {
		k := key{r.SensorID, r.Timestamp.Truncate(d).UnixNano()}
		g, ok := groups[k]
		if !ok {
			copied := r
			copied.Timestamp = r.Timestamp.Truncate(d)
			groups[k] = &copied
			order = append(order, k)
			g = &copied
		}
		if r.ID < g.ID {
			g.ID = r.ID
		}
		counts[k]++
		sums[k] += r.Value
	}
	out := make([]model.SensorReading, 0, len(order))
	for _, k := range order {
		g := *groups[k]
		g.Value = math.Round(sums[k]/float64(counts[k])*100) / 100
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.After(out[j].Timestamp)
		}
		return out[i].SensorID < out[j].SensorID
	})
	return out
}

// matchesSensor reports whether the reading's sensor matches the name/id search of f.
func (r *SensorRepository) matchesSensor(rd model.SensorReading, f repository.SensorHistoryFilter) bool {
	if f.SensorName == "" {
		return true
	}
	if f.SensorID != nil && rd.SensorID == *f.SensorID {
		return true
	}
	for _, s := range r.s.sensors {
		if s.ID == rd.SensorID {
			return strings.Contains(strings.ToLower(s.Name), strings.ToLower(f.SensorName))
		}
	}
	return false
}

// matchesAny reports whether the reading satisfies at least one condition of a; an empty union
// matches everything.
func (r *SensorRepository) matchesAny(rd model.SensorReading, a *repository.SensorAnyOf) bool {
	if a == nil {
		return true
	}
	hasSensor := a.SensorName != ""
	hasValue := a.ValueFrom != nil && a.ValueTo != nil
	hasTime := a.From != nil && a.To != nil
	if !hasSensor && !hasValue && !hasTime {
		return true
	}
	return (hasSensor && r.matchesSensor(rd, repository.SensorHistoryFilter{SensorName: a.SensorName, SensorID: a.SensorID})) ||
		(hasValue && rd.Value >= *a.ValueFrom && rd.Value < *a.ValueTo) ||
		(hasTime && !rd.Timestamp.Before(*a.From) && !rd.Timestamp.After(*a.To))
}

// ── Devices ──

type DeviceRepository struct{ s *Store }

func (r *DeviceRepository) FindByID(_ context.Context, id int64) (*model.Device, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	d, ok := r.s.devices[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	copied := *d
	return &copied, nil
}

func (r *DeviceRepository) List(_ context.Context) ([]model.Device, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := make([]model.Device, 0, len(r.s.devices))
	for id := int64(1); int(id) <= len(r.s.devices); id++ {
		out = append(out, *r.s.devices[id])
	}
	return out, nil
}

func (r *DeviceRepository) UpdateStatus(_ context.Context, id int64, status model.DeviceStatus) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	d, ok := r.s.devices[id]
	if !ok {
		return repository.ErrNotFound
	}
	d.Status = status
	d.UpdatedAt = r.s.clockFunc()
	return nil
}

func (r *DeviceRepository) CreateAction(_ context.Context, a *model.DeviceAction) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	a.ID = r.s.nextID("device_actions")
	a.Timestamp = r.s.clockFunc()
	r.s.actions = append(r.s.actions, *a)
	return nil
}

func (r *DeviceRepository) CompleteAction(
	_ context.Context, id int64, result model.ActionResult, message string,
) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for i := range r.s.actions {
		if r.s.actions[i].ID == id {
			now := r.s.clockFunc()
			r.s.actions[i].Result = result
			r.s.actions[i].Message = &message
			r.s.actions[i].CompletedAt = &now
			return nil
		}
	}
	return repository.ErrNotFound
}

func (r *DeviceRepository) ActionHistory(
	_ context.Context, f repository.ActionHistoryFilter,
) ([]model.DeviceActionHistory, int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var all []model.DeviceActionHistory
	for _, a := range r.s.actions {
		if (f.DeviceID == nil || a.DeviceID == *f.DeviceID) &&
			(f.From == nil || !a.Timestamp.Before(*f.From)) &&
			(f.To == nil || !a.Timestamp.After(*f.To)) &&
			(f.Action == nil || a.Action == *f.Action) &&
			(f.Result == nil || a.Result == *f.Result) {
			all = append(all, model.DeviceActionHistory{DeviceAction: a, DeviceName: r.s.devices[a.DeviceID].Name})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].Timestamp.Equal(all[j].Timestamp) {
			return all[i].Timestamp.After(all[j].Timestamp)
		}
		return all[i].ID > all[j].ID
	})
	return page(all, f.Offset, f.Limit), int64(len(all)), nil
}

func page[T any](all []T, offset, limit int) []T {
	if offset >= len(all) {
		return []T{}
	}
	end := min(offset+limit, len(all))
	return all[offset:end]
}
