package observers_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/metrics"
	"github.com/go42-dev/go42/internal/metrics/observers"
)

func TestDatabaseObserverUpdatesPoolMetrics(t *testing.T) {
	for _, test := range []struct {
		name     string
		named    bool
		interval time.Duration
		wantTick time.Duration
	}{
		{name: "unnamed", interval: time.Second, wantTick: time.Second},
		{name: "named", named: true, interval: 3 * time.Second, wantTick: 3 * time.Second},
		{name: "default interval", named: true, wantTick: 5 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				db := newObserverTestDB(t)
				db.SetMaxOpenConns(5)
				db.SetMaxIdleConns(5)
				name := ""
				labels := map[string]any{}
				if test.named {
					name = t.Name()
					labels["db_name"] = name
				}
				maxOpen := metrics.Gauge("go_sql_max_open_connections", labels)
				maxOpen.Set(-1)
				metrics.Counter("go_sql_wait_count_total", labels).Set(99)
				stop := observeDatabaseForTest(t, db, name, test.interval)
				first := observerTestConnection(t, db)
				second := observerTestConnection(t, db)
				third := observerTestConnection(t, db)
				require.NoError(t, first.Close())

				time.Sleep(test.wantTick - time.Nanosecond)
				synctest.Wait()
				assert.Equal(t, float64(-1), maxOpen.Get(), "metrics must wait for the first tick")
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				assertObservedDatabaseStats(t, labels, sql.DBStats{
					MaxOpenConnections: 5, OpenConnections: 3, InUse: 2, Idle: 1,
				})

				db.SetMaxIdleConns(0)
				require.NoError(t, second.Close())
				require.NoError(t, third.Close())
				db.SetMaxOpenConns(2)
				assert.Equal(
					t,
					float64(5),
					maxOpen.Get(),
					"metrics must retain the previous sample until the next tick",
				)
				time.Sleep(test.wantTick)
				synctest.Wait()
				want := sql.DBStats{MaxOpenConnections: 2, MaxIdleClosed: 3}
				assertObservedDatabaseStats(t, labels, want)

				stop()
				db.SetMaxOpenConns(9)
				time.Sleep(2 * test.wantTick)
				synctest.Wait()
				assertObservedDatabaseStats(t, labels, want)
			})
		})
	}
}

func TestDatabaseObserverRecordsConnectionWaits(t *testing.T) {
	for _, name := range []string{"connection released", "waiting request times out"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				db := newObserverTestDB(t)
				db.SetMaxOpenConns(1)
				db.SetMaxIdleConns(1)
				observeDatabaseForTest(t, db, t.Name(), 5*time.Second)
				held := observerTestConnection(t, db)
				waitCtx := t.Context()
				if name == "waiting request times out" {
					ctx, cancel := context.WithTimeout(waitCtx, 2500*time.Millisecond)
					defer cancel()
					waitCtx = ctx
				}
				result := make(chan error, 1)
				go func() {
					conn, err := db.Conn(waitCtx)
					if err == nil {
						err = conn.Close()
					}
					result <- err
				}()
				synctest.Wait()
				time.Sleep(2500 * time.Millisecond)
				synctest.Wait()
				if name == "waiting request times out" {
					require.ErrorIs(t, <-result, context.DeadlineExceeded)
					require.NoError(t, held.Close())
				} else {
					require.NoError(t, held.Close())
					require.NoError(t, <-result)
				}
				time.Sleep(2500 * time.Millisecond)
				synctest.Wait()
				assertObservedDatabaseStats(t, map[string]any{"db_name": t.Name()}, sql.DBStats{
					MaxOpenConnections: 1, OpenConnections: 1, Idle: 1,
					WaitCount: 1, WaitDuration: 2 * time.Second,
				})
			})
		})
	}
}

func TestDatabaseObserverRecordsRetiredConnections(t *testing.T) {
	for _, test := range []struct {
		name        string
		connections int
		idleTimeout bool
		want        sql.DBStats
	}{
		{
			name: "idle timeout", connections: 3, idleTimeout: true,
			want: sql.DBStats{MaxOpenConnections: 4, MaxIdleTimeClosed: 3},
		},
		{
			name: "maximum lifetime", connections: 2,
			want: sql.DBStats{MaxOpenConnections: 4, MaxLifetimeClosed: 2},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				db := newObserverTestDB(t)
				db.SetMaxOpenConns(4)
				db.SetMaxIdleConns(4)
				observeDatabaseForTest(t, db, t.Name(), 10*time.Second)
				connections := make([]*sql.Conn, 0, test.connections)
				for range test.connections {
					connections = append(connections, observerTestConnection(t, db))
				}
				for _, conn := range connections {
					require.NoError(t, conn.Close())
				}
				if test.idleTimeout {
					db.SetConnMaxIdleTime(2 * time.Second)
				} else {
					db.SetConnMaxLifetime(2 * time.Second)
				}
				synctest.Wait()
				time.Sleep(10 * time.Second)
				synctest.Wait()
				assertObservedDatabaseStats(t, map[string]any{"db_name": t.Name()}, test.want)
			})
		})
	}
}

func TestDatabaseObserverCancellationBeforeFirstTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := newObserverTestDB(t)
		labels := map[string]any{"db_name": t.Name()}
		maxOpen := metrics.Gauge("go_sql_max_open_connections", labels)
		maxOpen.Set(-1)
		stop := observeDatabaseForTest(t, db, t.Name(), time.Second)
		stop()
		db.SetMaxOpenConns(42)
		time.Sleep(10 * time.Second)
		synctest.Wait()
		assert.Equal(t, float64(-1), maxOpen.Get())
	})
}

func observeDatabaseForTest(t *testing.T, db *sql.DB, name string, interval time.Duration) func() {
	t.Helper()
	observer, err := observers.NewDatabaseObserver(
		db,
		observers.WithName(name),
		observers.WithObserveInterval(interval),
	)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		observer.Observe(ctx)
	}()
	stop := func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	synctest.Wait()
	return stop
}

func assertObservedDatabaseStats(t *testing.T, labels map[string]any, want sql.DBStats) {
	t.Helper()
	for name, value := range map[string]float64{
		"go_sql_max_open_connections": float64(want.MaxOpenConnections),
		"go_sql_open_connections":     float64(want.OpenConnections),
		"go_sql_in_use_connections":   float64(want.InUse),
		"go_sql_idle_connections":     float64(want.Idle),
	} {
		assert.Equal(t, value, metrics.Gauge(name, labels).Get(), name)
	}
	for name, value := range map[string]uint64{
		"go_sql_wait_count_total":            uint64(want.WaitCount),
		"go_sql_wait_duration_seconds_total": uint64(want.WaitDuration / time.Second),
		"go_sql_max_idle_closed_total":       uint64(want.MaxIdleClosed),
		"go_sql_idle_time_closed_total":      uint64(want.MaxIdleTimeClosed),
		"go_sql_lifetime_closed_total":       uint64(want.MaxLifetimeClosed),
	} {
		assert.Equal(t, value, metrics.Counter(name, labels).Get(), name)
	}
}

func newObserverTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(observerTestDriver{})
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func observerTestConnection(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := conn.Close(); !errors.Is(err, sql.ErrConnDone) {
			assert.NoError(t, err)
		}
	})
	return conn
}

// Only the standard library's connection pool runs; the driver performs no I/O.
type observerTestDriver struct{}

func (d observerTestDriver) Driver() driver.Driver { return d }

func (observerTestDriver) Open(string) (driver.Conn, error) {
	return observerTestConn{}, nil
}

func (observerTestDriver) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return observerTestConn{}, nil
}

type observerTestConn struct{}

func (observerTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("SQL statements are not supported by the observer test driver")
}

func (observerTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported by the observer test driver")
}

func (observerTestConn) Close() error { return nil }
