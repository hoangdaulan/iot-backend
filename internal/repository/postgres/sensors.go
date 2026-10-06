package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"iot-backend/internal/model"
	"iot-backend/internal/repository"
)

type SensorRepository struct{ pool *pgxpool.Pool }

func NewSensorRepository(pool *pgxpool.Pool) *SensorRepository {
	return &SensorRepository{pool: pool}
}

func (r *SensorRepository) List(ctx context.Context) ([]model.Sensor, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, type, unit, status, mqtt_topic, created_at, updated_at
		FROM sensors ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list sensors: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Sensor, error) {
		var s model.Sensor
		err := row.Scan(&s.ID, &s.Name, &s.Type, &s.Unit, &s.Status, &s.MQTTTopic,
			&s.CreatedAt, &s.UpdatedAt)
		return s, err
	})
}

func (r *SensorRepository) InsertData(ctx context.Context, data []model.SensorData) error {
	if len(data) == 0 {
		return nil
	}
	_, err := r.pool.CopyFrom(ctx,
		pgx.Identifier{"sensor_data"},
		[]string{"sensor_id", "value", "timestamp"},
		pgx.CopyFromSlice(len(data), func(i int) ([]any, error) {
			return []any{data[i].SensorID, data[i].Value, data[i].Timestamp}, nil
		}),
	)
	if err != nil {
		return fmt.Errorf("insert sensor data: %w", err)
	}
	return nil
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

const readingColumns = `d.id, d.sensor_id, d.value, d.timestamp, s.type, s.unit`

func scanReading(row pgx.CollectableRow) (model.SensorReading, error) {
	var r model.SensorReading
	err := row.Scan(&r.ID, &r.SensorID, &r.Value, &r.Timestamp, &r.Type, &r.Unit)
	return r, err
}

func (r *SensorRepository) Latest(ctx context.Context) ([]model.SensorReading, error) {
	// One index lookup per sensor on (sensor_id, timestamp DESC).
	rows, err := r.pool.Query(ctx, `
		SELECT `+readingColumns+`
		FROM sensors s
		CROSS JOIN LATERAL (
			SELECT id, sensor_id, value, timestamp FROM sensor_data
			WHERE sensor_id = s.id
			ORDER BY timestamp DESC, id DESC
			LIMIT 1
		) d
		ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("latest sensor data: %w", err)
	}
	return pgx.CollectRows(rows, scanReading)
}

func (r *SensorRepository) History(
	ctx context.Context, f repository.SensorHistoryFilter,
) ([]model.SensorReading, int64, error) {
	var where []string
	var args []any
	add := func(clause string, arg any) {
		args = append(args, arg)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.Type != nil {
		add("s.type = $%d", string(*f.Type))
	}
	if f.From != nil {
		add("d.timestamp >= $%d", *f.From)
	}
	if f.To != nil {
		add("d.timestamp <= $%d", *f.To)
	}
	if f.Value != nil {
		add("d.value = $%d", *f.Value)
	}
	if f.ValueFrom != nil {
		add("d.value >= $%d", *f.ValueFrom)
	}
	if f.ValueTo != nil {
		add("d.value < $%d", *f.ValueTo)
	}
	if f.SensorName != "" {
		args = append(args, "%"+likeEscaper.Replace(f.SensorName)+"%")
		clause := fmt.Sprintf("s.name ILIKE $%d", len(args))
		if f.SensorID != nil {
			args = append(args, *f.SensorID)
			clause = fmt.Sprintf("(%s OR s.id = $%d)", clause, len(args))
		}
		where = append(where, clause)
	}
	if a := f.Any; a != nil {
		var any []string
		if a.SensorName != "" {
			args = append(args, "%"+likeEscaper.Replace(a.SensorName)+"%")
			clause := fmt.Sprintf("s.name ILIKE $%d", len(args))
			if a.SensorID != nil {
				args = append(args, *a.SensorID)
				clause += fmt.Sprintf(" OR s.id = $%d", len(args))
			}
			any = append(any, "("+clause+")")
		}
		if a.ValueFrom != nil && a.ValueTo != nil {
			args = append(args, *a.ValueFrom, *a.ValueTo)
			any = append(any, fmt.Sprintf("(d.value >= $%d AND d.value < $%d)", len(args)-1, len(args)))
		}
		if a.From != nil && a.To != nil {
			args = append(args, *a.From, *a.To)
			any = append(any, fmt.Sprintf("(d.timestamp >= $%d AND d.timestamp <= $%d)", len(args)-1, len(args)))
		}
		if len(any) > 0 {
			where = append(where, "("+strings.Join(any, " OR ")+")")
		}
	}
	from := ` FROM sensor_data d JOIN sensors s ON s.id = d.sensor_id`
	if len(where) > 0 {
		from += " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count sensor history: %w", err)
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := r.pool.Query(ctx,
		`SELECT `+readingColumns+from+
			fmt.Sprintf(` ORDER BY d.timestamp DESC, s.id, d.id DESC LIMIT $%d OFFSET $%d`,
				len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, fmt.Errorf("sensor history: %w", err)
	}
	readings, err := pgx.CollectRows(rows, scanReading)
	return readings, total, err
}
