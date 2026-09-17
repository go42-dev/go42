package observers

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go42-dev/go42/internal/metrics"
)

const defaultObserveInterval = 5 * time.Second

type DatabaseObserver struct {
	db    *sql.DB
	every time.Duration
	name  string
}

func NewDatabaseObserver(db *sql.DB, opts ...DatabaseObserverOption) (*DatabaseObserver, error) {
	o := &DatabaseObserver{
		db: db,
	}
	for _, opt := range opts {
		opt(o)
	}

	if o.every == 0 {
		o.every = defaultObserveInterval
	}

	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}
	if db.Ping() != nil {
		return nil, fmt.Errorf("database is not reachable")
	}

	return o, nil
}

type DatabaseObserverOption func(*DatabaseObserver)

// WithObserveInterval sets the interval at which the database metrics are updated.
func WithObserveInterval(d time.Duration) DatabaseObserverOption {
	return func(o *DatabaseObserver) {
		o.every = d
	}
}

// WithName sets the name of the database in label named 'db_name'
func WithName(name string) DatabaseObserverOption {
	return func(o *DatabaseObserver) {
		o.name = name
	}
}

// Observe starts collection of metrics.
func (o *DatabaseObserver) Observe(ctx context.Context) {
	labels := make(map[string]any, 0)
	if o.name != "" {
		labels["db_name"] = o.name
	}
	ticker := time.NewTicker(o.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.updateDBMetrics(labels)
		}
	}
}

// #nosec | Negative values are not expected.
func (o *DatabaseObserver) updateDBMetrics(labels map[string]any) {
	sqlStats := o.db.Stats()
	metrics.
		Gauge("go_sql_max_open_connections", labels).
		Set(float64(sqlStats.MaxOpenConnections))
	metrics.
		Gauge("go_sql_open_connections", labels).
		Set(float64(sqlStats.OpenConnections))
	metrics.
		Gauge("go_sql_in_use_connections", labels).
		Set(float64(sqlStats.InUse))
	metrics.
		Gauge("go_sql_idle_connections", labels).
		Set(float64(sqlStats.Idle))
	metrics.
		Counter("go_sql_wait_count_total", labels).
		Set(uint64(sqlStats.WaitCount))
	metrics.
		Counter("go_sql_wait_duration_seconds_total", labels).
		Set(uint64(sqlStats.WaitDuration.Seconds()))
	metrics.
		Counter("go_sql_max_idle_closed_total", labels).
		Set(uint64(sqlStats.MaxIdleClosed))
	metrics.
		Counter("go_sql_idle_time_closed_total", labels).
		Set(uint64(sqlStats.MaxIdleTimeClosed))
	metrics.
		Counter("go_sql_lifetime_closed_total", labels).
		Set(uint64(sqlStats.MaxLifetimeClosed))
}
