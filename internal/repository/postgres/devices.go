package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"iot-backend/internal/model"
	"iot-backend/internal/repository"
)

type DeviceRepository struct{ pool *pgxpool.Pool }

func NewDeviceRepository(pool *pgxpool.Pool) *DeviceRepository {
	return &DeviceRepository{pool: pool}
}

func (r *DeviceRepository) FindByID(ctx context.Context, id int64) (*model.Device, error) {
	var d model.Device
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, type, status, created_at, updated_at
		FROM devices WHERE id = $1`, id,
	).Scan(&d.ID, &d.Name, &d.Type, &d.Status, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find device: %w", err)
	}
	return &d, nil
}

func (r *DeviceRepository) List(ctx context.Context) ([]model.Device, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, type, status, created_at, updated_at FROM devices ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Device, error) {
		var d model.Device
		err := row.Scan(&d.ID, &d.Name, &d.Type, &d.Status, &d.CreatedAt, &d.UpdatedAt)
		return d, err
	})
}

func (r *DeviceRepository) UpdateStatus(
	ctx context.Context, id int64, status model.DeviceStatus,
) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE devices SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("update device status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *DeviceRepository) CreateAction(ctx context.Context, a *model.DeviceAction) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO device_actions (user_id, device_id, action, result, message)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, timestamp`,
		a.UserID, a.DeviceID, a.Action, a.Result, a.Message,
	).Scan(&a.ID, &a.Timestamp)
	if err != nil {
		return fmt.Errorf("insert device action: %w", err)
	}
	return nil
}

func (r *DeviceRepository) CompleteAction(
	ctx context.Context, id int64, result model.ActionResult, message string,
) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE device_actions SET result = $2, message = $3, completed_at = now()
		WHERE id = $1`, id, result, message)
	if err != nil {
		return fmt.Errorf("complete device action: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *DeviceRepository) ActionHistory(
	ctx context.Context, f repository.ActionHistoryFilter,
) ([]model.DeviceActionHistory, int64, error) {
	var where []string
	var args []any
	add := func(clause string, arg any) {
		args = append(args, arg)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.DeviceID != nil {
		add("a.device_id = $%d", *f.DeviceID)
	}
	if f.From != nil {
		add("a.timestamp >= $%d", *f.From)
	}
	if f.To != nil {
		add("a.timestamp <= $%d", *f.To)
	}
	if f.Action != nil {
		add("a.action = $%d", string(*f.Action))
	}
	if f.Result != nil {
		add("a.result = $%d", string(*f.Result))
	}
	from := ` FROM device_actions a JOIN devices d ON d.id = a.device_id LEFT JOIN users u ON u.id = a.user_id`
	if len(where) > 0 {
		from += " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count device actions: %w", err)
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.user_id, a.device_id, a.action, a.result, a.message, a.timestamp,
			a.completed_at, d.name, u.id, u.username, u.name, u.role`+from+
		fmt.Sprintf(` ORDER BY a.timestamp DESC, a.id DESC LIMIT $%d OFFSET $%d`,
			len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, fmt.Errorf("device action history: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.DeviceActionHistory, error) {
		var h model.DeviceActionHistory
		var userID *int64
		var username, userName, role *string
		err := row.Scan(&h.ID, &h.UserID, &h.DeviceID, &h.Action, &h.Result, &h.Message,
			&h.Timestamp, &h.CompletedAt, &h.DeviceName, &userID, &username, &userName, &role)
		if userID != nil {
			h.User = &model.User{ID: *userID, Username: *username, Name: userName, Role: model.Role(*role)}
		}
		return h, err
	})
	return items, total, err
}
