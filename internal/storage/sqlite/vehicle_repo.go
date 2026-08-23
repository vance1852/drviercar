package sqlite

import (
	"context"
	"database/sql"
	"strings"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
)

type vehicleRepo struct {
	q queryer
}

const vehicleColumns = `id, plate, autonomy, status, home_depot, odometer_km, sensor_profile,
	version, created_at, updated_at`

var vehicleSortColumns = map[string]string{
	"plate":       "plate",
	"odometer_km": "odometer_km",
	"created_at":  "created_at",
	"status":      "status",
}

func (r *vehicleRepo) Create(ctx context.Context, vehicle *domain.Vehicle) (int64, error) {
	if err := vehicle.Validate(); err != nil {
		return 0, err
	}
	result, err := r.q.ExecContext(ctx, `
		INSERT INTO vehicles (plate, autonomy, status, home_depot, odometer_km, sensor_profile,
			version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		vehicle.Plate, string(vehicle.Autonomy), string(vehicle.Status), vehicle.HomeDepot,
		vehicle.OdometerKm, encodeSensorProfile(vehicle.SensorProfile),
		1, toUnix(vehicle.CreatedAt), toUnix(vehicle.UpdatedAt))
	if err != nil {
		return 0, translate(err, "vehicle_create_failed", "车牌 "+vehicle.Plate+" 已登记")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.KindInternal, "vehicle_create_failed", "无法读取车辆标识")
	}
	return id, nil
}

func (r *vehicleRepo) ByID(ctx context.Context, id int64) (*domain.Vehicle, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+vehicleColumns+` FROM vehicles WHERE id = ?`, id)
	return scanVehicleRow(row)
}

func (r *vehicleRepo) ByPlate(ctx context.Context, plate string) (*domain.Vehicle, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+vehicleColumns+` FROM vehicles WHERE plate = ?`, plate)
	return scanVehicleRow(row)
}

func (r *vehicleRepo) UpdateStatus(ctx context.Context, id int64, expectedVersion int64, status domain.VehicleStatus) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE vehicles SET status = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ?`,
		string(status), nowMicro(), id, expectedVersion)
	if err != nil {
		return translate(err, "vehicle_status_update_failed", "无法更新车辆状态")
	}
	return affectedOne(result, "vehicle_version_conflict", "车辆已被其他操作修改，请刷新后重试")
}

func (r *vehicleRepo) AddOdometer(ctx context.Context, id int64, expectedVersion int64, deltaKm float64) error {
	if deltaKm < 0 {
		return apperr.Invalidf("vehicle_odometer_delta_invalid", "里程增量不能为负数")
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE vehicles SET odometer_km = odometer_km + ?, version = version + 1, updated_at = ?
		WHERE id = ? AND version = ?`,
		deltaKm, nowMicro(), id, expectedVersion)
	if err != nil {
		return translate(err, "vehicle_odometer_update_failed", "无法累加车辆里程")
	}
	return affectedOne(result, "vehicle_version_conflict", "车辆已被其他操作修改，请刷新后重试")
}

func (r *vehicleRepo) List(ctx context.Context, filter repository.VehicleFilter) ([]*domain.Vehicle, int, error) {
	page, err := filter.Page.Normalize(vehicleSortColumns, "plate")
	if err != nil {
		return nil, 0, err
	}
	where, args := vehicleFilterClause(filter)

	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT COUNT(1) FROM vehicles`+where, args...).Scan(&total); err != nil {
		return nil, 0, translate(err, "vehicle_list_failed", "无法统计车辆")
	}

	listArgs := append(append([]any{}, args...), page.PageSize, page.Offset())
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+vehicleColumns+` FROM vehicles`+where+
			` ORDER BY `+page.OrderClause(vehicleSortColumns)+`, id ASC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, translate(err, "vehicle_list_failed", "无法查询车辆")
	}
	defer rows.Close()

	vehicles := make([]*domain.Vehicle, 0, page.PageSize)
	for rows.Next() {
		vehicle, scanErr := scanVehicleRows(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		vehicles = append(vehicles, vehicle)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, translate(err, "vehicle_list_failed", "读取车辆失败")
	}
	return vehicles, total, nil
}

func vehicleFilterClause(filter repository.VehicleFilter) (string, []any) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if depot := strings.TrimSpace(filter.Depot); depot != "" {
		conditions = append(conditions, "home_depot = ?")
		args = append(args, depot)
	}
	if len(filter.Statuses) > 0 {
		conditions = append(conditions, "status IN ("+placeholders(len(filter.Statuses))+")")
		for _, status := range filter.Statuses {
			args = append(args, string(status))
		}
	}
	if filter.Autonomy != "" {
		conditions = append(conditions, "autonomy = ?")
		args = append(args, string(filter.Autonomy))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func encodeSensorProfile(profile []string) string {
	cleaned := make([]string, 0, len(profile))
	for _, sensor := range profile {
		trimmed := strings.TrimSpace(sensor)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return strings.Join(cleaned, ",")
}

func decodeSensorProfile(encoded string) []string {
	if strings.TrimSpace(encoded) == "" {
		return nil
	}
	parts := strings.Split(encoded, ",")
	profile := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			profile = append(profile, trimmed)
		}
	}
	return profile
}

func scanVehicleRow(row *sql.Row) (*domain.Vehicle, error) {
	var (
		vehicle domain.Vehicle
		autonomy string
		status   string
		sensors  string
		created  int64
		updated  int64
	)
	err := row.Scan(&vehicle.ID, &vehicle.Plate, &autonomy, &status, &vehicle.HomeDepot,
		&vehicle.OdometerKm, &sensors, &vehicle.Version, &created, &updated)
	if err != nil {
		return nil, translate(err, "vehicle_not_found", "车辆不存在")
	}
	vehicle.Autonomy = domain.AutonomyLevel(autonomy)
	vehicle.Status = domain.VehicleStatus(status)
	vehicle.SensorProfile = decodeSensorProfile(sensors)
	vehicle.CreatedAt = fromUnix(created)
	vehicle.UpdatedAt = fromUnix(updated)
	return vehicle.Clone(), nil
}

func scanVehicleRows(rows *sql.Rows) (*domain.Vehicle, error) {
	var (
		vehicle  domain.Vehicle
		autonomy string
		status   string
		sensors  string
		created  int64
		updated  int64
	)
	err := rows.Scan(&vehicle.ID, &vehicle.Plate, &autonomy, &status, &vehicle.HomeDepot,
		&vehicle.OdometerKm, &sensors, &vehicle.Version, &created, &updated)
	if err != nil {
		return nil, translate(err, "vehicle_list_failed", "读取车辆失败")
	}
	vehicle.Autonomy = domain.AutonomyLevel(autonomy)
	vehicle.Status = domain.VehicleStatus(status)
	vehicle.SensorProfile = decodeSensorProfile(sensors)
	vehicle.CreatedAt = fromUnix(created)
	vehicle.UpdatedAt = fromUnix(updated)
	return vehicle.Clone(), nil
}
