package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/goleak"

	"github.com/go42-dev/go42/internal/config"
)

const (
	appTestTimeout             = 5 * time.Second
	shutdownSubprocessTimeout  = 15 * time.Second
	shutdownSubprocessEnv      = "GO42_TEST_SHUTDOWN_CHILD"
	shutdownSecondSignalMarker = "ready for second shutdown signal"
)

type readinessWatchTestCase struct {
	name        string
	observables []<-chan error
}

type shutdownTestFunc func(context.Context) error

func (fn shutdownTestFunc) Shutdown(ctx context.Context) error {
	return fn(ctx)
}

type shutdownTestCloser func() error

func (fn shutdownTestCloser) Close() error {
	return fn()
}

type shutdownWrapTestCase struct {
	name       string
	withCloser bool
	withFunc   bool
	failure    error
}

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestWatchReadinessRecordsFailureFromAnyChannel(t *testing.T) {
	for failedChannel := range 3 {
		t.Run(fmt.Sprintf("channel_%d", failedChannel), func(t *testing.T) {
			ctx := t.Context()
			readinessCtx, markUnready := context.WithCancelCause(ctx)
			failure := errors.New("component failed")
			observables := make([]<-chan error, 3)
			for i := range observables {
				observable := make(chan error, 1)
				observables[i] = observable
				if i == failedChannel {
					observable <- failure
				}
			}

			done := startReadinessWatcher(ctx, markUnready, observables...)
			waitForReadinessWatcher(t, done)

			if err := context.Cause(readinessCtx); !errors.Is(err, failure) {
				t.Fatalf("readiness error = %v, want component failure", err)
			}
			if err := ctx.Err(); err != nil {
				t.Fatalf("component failure canceled the application context: %v", err)
			}
		})
	}
}

func TestWatchReadinessReturnsWithoutActiveChannels(t *testing.T) {
	closed := make(chan error)
	close(closed)
	tests := []readinessWatchTestCase{
		{name: "no_channels"},
		{name: "nil_channels", observables: []<-chan error{nil, nil}},
		{name: "closed_channels", observables: []<-chan error{closed, nil, closed}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			readinessCtx, markUnready := context.WithCancelCause(ctx)
			done := startReadinessWatcher(ctx, markUnready, test.observables...)
			waitForReadinessWatcher(t, done)

			if err := context.Cause(readinessCtx); err != nil {
				t.Fatalf("readiness failed without a component error: %v", err)
			}
		})
	}
}

func TestWatchReadinessContinuesAfterCleanStopsAndNilErrors(t *testing.T) {
	ctx := t.Context()
	readinessCtx, markUnready := context.WithCancelCause(ctx)
	closed := make(chan error)
	close(closed)
	failure := errors.New("component failed")
	failures := make(chan error, 1<<1)
	failures <- nil
	failures <- failure
	close(failures)

	done := startReadinessWatcher(ctx, markUnready, closed, nil, failures)
	waitForReadinessWatcher(t, done)

	if err := context.Cause(readinessCtx); !errors.Is(err, failure) {
		t.Fatalf("readiness error = %v, want component failure", err)
	}
}

func TestWatchReadinessReportsOnlyOneConcurrentFailure(t *testing.T) {
	ctx := t.Context()
	readinessCtx, markUnready := context.WithCancelCause(ctx)
	firstFailure := errors.New("first component failed")
	secondFailure := errors.New("second component failed")
	first := make(chan error, 1)
	second := make(chan error, 1)
	first <- firstFailure
	second <- secondFailure
	var calls atomic.Int32

	done := startReadinessWatcher(ctx, func(err error) {
		calls.Add(1)
		markUnready(err)
	}, first, second)
	waitForReadinessWatcher(t, done)

	if got := calls.Load(); got != 1 {
		t.Fatalf("readiness failure calls = %d, want 1", got)
	}
	if err := context.Cause(readinessCtx); !errors.Is(err, firstFailure) && !errors.Is(err, secondFailure) {
		t.Fatalf("readiness error = %v, want one of the component failures", err)
	}
}

func TestWatchReadinessStopsWithApplicationContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := startReadinessWatcher(ctx, func(err error) {
		t.Errorf("application shutdown reported a component failure: %v", err)
	}, make(chan error), nil, make(chan error))

	cancel()
	waitForReadinessWatcher(t, done)
}

func TestWatchReadinessTracksRemainingOpenChannels(t *testing.T) {
	for _, outcome := range []string{"failure", "closure"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				readinessCtx, readinessCancel := context.WithCancelCause(ctx)
				first := make(chan error)
				second := make(chan error)
				done := startReadinessWatcher(ctx, readinessCancel, first, second)
				synctest.Wait()
				assertReadinessWatcherRunning(t, done, readinessCtx)

				first <- nil
				synctest.Wait()
				assertReadinessWatcherRunning(t, done, readinessCtx)

				close(first)
				synctest.Wait()
				assertReadinessWatcherRunning(t, done, readinessCtx)

				var want error
				if outcome == "failure" {
					want = errors.New("remaining component failed")
					second <- want
				} else {
					close(second)
				}
				synctest.Wait()
				waitForReadinessWatcher(t, done)
				if err := context.Cause(readinessCtx); !errors.Is(err, want) {
					t.Errorf("readiness error = %v, want %v", err, want)
				}
			})
		})
	}
}

func TestWatchReadinessIgnoresQueuedFailuresAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	readinessCtx, readinessCancel := context.WithCancelCause(ctx)
	parentFailure := errors.New("application stopped")
	cancel(parentFailure)

	observables := make([]<-chan error, 32)
	for i := range observables {
		observable := make(chan error, 1)
		observable <- errors.New("queued component failure")
		close(observable)
		observables[i] = observable
	}
	var calls atomic.Int32
	done := startReadinessWatcher(ctx, func(err error) {
		calls.Add(1)
		readinessCancel(err)
	}, observables...)
	waitForReadinessWatcher(t, done)

	if got := calls.Load(); got != 0 {
		t.Errorf("readiness cancellation called %d times after application cancellation", got)
	}
	if err := context.Cause(readinessCtx); !errors.Is(err, parentFailure) {
		t.Errorf("readiness cause = %v, want application cancellation cause", err)
	}
}

func TestWatchReadinessStopsAtParentDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		readinessCtx, readinessCancel := context.WithCancelCause(ctx)
		var calls atomic.Int32
		done := startReadinessWatcher(ctx, func(err error) {
			calls.Add(1)
			readinessCancel(err)
		}, make(chan error), make(chan error))
		synctest.Wait()
		assertReadinessWatcherRunning(t, done, readinessCtx)

		time.Sleep(time.Second)
		synctest.Wait()
		waitForReadinessWatcher(t, done)
		if got := calls.Load(); got != 0 {
			t.Errorf("parent deadline reported %d component failures", got)
		}
		if err := context.Cause(readinessCtx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("readiness cause = %v, want parent deadline", err)
		}
	})
}

func TestShutdownAcceptsSignals(t *testing.T) {
	if runShutdownSubprocess(t, 0) {
		return
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			cfg := shutdownTestConfig()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var cancelCalls int
			var order []string
			var cleanupContexts []context.Context
			first := shutdownTestFunc(func(cleanupCtx context.Context) error {
				if !errors.Is(ctx.Err(), context.Canceled) {
					t.Error("application context was not canceled before cleanup")
				}
				if cleanupCtx.Err() != nil {
					t.Errorf("cleanup inherited application cancellation: %v", cleanupCtx.Err())
				}
				if deadline, ok := cleanupCtx.Deadline(); !ok ||
					time.Until(deadline) > cfg.Core.ShutdownComponentTimeout {
					t.Error("cleanup context does not respect the component timeout")
				}
				cleanupContexts = append(cleanupContexts, cleanupCtx)
				order = append(order, "first")
				return nil
			})
			second := shutdownTestFunc(func(cleanupCtx context.Context) error {
				if !errors.Is(cleanupContexts[0].Err(), context.Canceled) {
					t.Error("previous component context was not canceled after cleanup")
				}
				if cleanupCtx.Err() != nil {
					t.Errorf("next component received a canceled context: %v", cleanupCtx.Err())
				}
				cleanupContexts = append(cleanupContexts, cleanupCtx)
				order = append(order, "second")
				return nil
			})
			started, done := startTestShutdown(cfg, func() {
				cancelCalls++
				cancel()
			}, nil, first, nil, second, nil)

			signalTestShutdown(t, sig, started)
			waitForTestSignal(t, done, "shutdown completion")
			if cancelCalls != 1 {
				t.Errorf("application cancellation calls = %d, want 1", cancelCalls)
			}
			if !slices.Equal(order, []string{"first", "second"}) {
				t.Errorf("cleanup order = %v, want [first second]", order)
			}
			for _, cleanupCtx := range cleanupContexts {
				if !errors.Is(cleanupCtx.Err(), context.Canceled) {
					t.Errorf("component context was not released: %v", cleanupCtx.Err())
				}
			}
		})
	}
}

func TestShutdownWaitsForSignalAfterReadinessFailure(t *testing.T) {
	if runShutdownSubprocess(t, 0) {
		return
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	readinessCtx, readinessCancel := context.WithCancelCause(ctx)
	failure := errors.New("component failed")
	observable := make(chan error, 1)
	observable <- failure
	watchDone := startReadinessWatcher(ctx, readinessCancel, observable)
	var closed atomic.Bool
	started, done := startTestShutdown(shutdownTestConfig(), cancel, shutdownTestFunc(func(context.Context) error {
		closed.Store(true)
		return nil
	}))
	waitForReadinessWatcher(t, watchDone)
	if err := context.Cause(readinessCtx); !errors.Is(err, failure) {
		t.Errorf("readiness cause = %v, want component failure", err)
	}

	select {
	case <-started:
		t.Error("shutdown started before an OS signal")
	case <-done:
		t.Error("shutdown returned before an OS signal")
	case <-time.After(20 * time.Millisecond):
	}
	if ctx.Err() != nil || closed.Load() {
		t.Error("readiness failure canceled the application or started cleanup")
	}

	signalTestShutdown(t, syscall.SIGTERM, started)
	waitForTestSignal(t, done, "shutdown completion")
	if !closed.Load() {
		t.Error("component was not closed after the OS signal")
	}
}

func TestShutdownDrainsBeforeClosingAndWaitsForCleanup(t *testing.T) {
	if runShutdownSubprocess(t, 0) {
		return
	}
	cfg := shutdownTestConfig()
	cfg.Core.ShutdownWaitForProbe = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var canceledAt time.Time
	started, done := startTestShutdown(cfg, func() {
		canceledAt = time.Now()
		cancel()
	}, shutdownTestFunc(func(context.Context) error {
		if elapsed := time.Since(canceledAt); elapsed < cfg.Core.ShutdownWaitForProbe {
			t.Errorf("cleanup started after %s, before probe drain delay %s", elapsed, cfg.Core.ShutdownWaitForProbe)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Error("application remained ready during probe drain")
		}
		close(entered)
		<-release
		return nil
	}))
	defer func() {
		close(release)
		waitForTestSignal(t, done, "cleanup after release")
	}()

	signalTestShutdown(t, syscall.SIGTERM, started)
	waitForTestSignal(t, entered, "component cleanup")
	select {
	case <-done:
		t.Error("shutdown returned while component cleanup was still running")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestShutdownContinuesAfterComponentError(t *testing.T) {
	if runShutdownSubprocess(t, 0) {
		return
	}
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)
	failure := errors.New("component cleanup failed")
	var order []string
	started, done := startTestShutdown(shutdownTestConfig(), func() {},
		shutdownTestFunc(func(context.Context) error {
			order = append(order, "failing")
			return failure
		}),
		shutdownTestFunc(func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Errorf("component error canceled subsequent cleanup: %v", ctx.Err())
			}
			order = append(order, "healthy")
			return nil
		}),
	)
	signalTestShutdown(t, syscall.SIGTERM, started)
	waitForTestSignal(t, done, "shutdown completion")

	if !slices.Equal(order, []string{"failing", "healthy"}) {
		t.Errorf("cleanup order = %v, want [failing healthy]", order)
	}
	if output := logs.String(); !strings.Contains(output, "shutdown error") ||
		!strings.Contains(output, failure.Error()) {
		t.Errorf("cleanup failure was not logged: %s", output)
	}
}

func TestShutdownContinuesAfterComponentTimeout(t *testing.T) {
	if runShutdownSubprocess(t, 0) {
		return
	}
	cfg := shutdownTestConfig()
	cfg.Core.ShutdownComponentTimeout = 25 * time.Millisecond
	var firstErr error
	var nextClosed bool
	started, done := startTestShutdown(cfg, func() {},
		shutdownTestFunc(func(ctx context.Context) error {
			<-ctx.Done()
			firstErr = ctx.Err()
			return firstErr
		}),
		shutdownTestFunc(func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Errorf("next component inherited the expired timeout: %v", ctx.Err())
			}
			nextClosed = true
			return nil
		}),
	)
	signalTestShutdown(t, syscall.SIGTERM, started)
	waitForTestSignal(t, done, "shutdown completion")

	if !errors.Is(firstErr, context.DeadlineExceeded) {
		t.Errorf("component error = %v, want deadline exceeded", firstErr)
	}
	if !nextClosed {
		t.Error("remaining component was skipped after a component timeout")
	}
}

func TestShutdownGracePeriodBoundsUnresponsiveComponent(t *testing.T) {
	if runShutdownSubprocess(t, 0) {
		return
	}
	cfg := shutdownTestConfig()
	cfg.Core.ShutdownGracePeriod = 50 * time.Millisecond
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var cleanupCtx context.Context
	started, done := startTestShutdown(cfg, func() {}, shutdownTestFunc(func(ctx context.Context) error {
		defer close(finished)
		cleanupCtx = ctx
		close(entered)
		<-release
		return nil
	}))
	defer func() {
		close(release)
		waitForTestSignal(t, finished, "release of stalled component")
	}()

	signalTestShutdown(t, syscall.SIGTERM, started)
	waitForTestSignal(t, entered, "component cleanup")
	waitForTestSignal(t, done, "overall shutdown deadline")
	if !errors.Is(cleanupCtx.Err(), context.DeadlineExceeded) {
		t.Errorf("component context = %v, want overall deadline exceeded", cleanupCtx.Err())
	}
	select {
	case <-finished:
		t.Error("stalled component unexpectedly completed before release")
	default:
	}
}

func TestShutdownGracePeriodIncludesProbeDrain(t *testing.T) {
	if runShutdownSubprocess(t, 0) {
		return
	}
	cfg := shutdownTestConfig()
	cfg.Core.ShutdownGracePeriod = 25 * time.Millisecond
	cfg.Core.ShutdownWaitForProbe = 150 * time.Millisecond
	cleaned := make(chan struct{})
	started, done := startTestShutdown(cfg, func() {}, shutdownTestFunc(func(ctx context.Context) error {
		defer close(cleaned)
		if ctx.Err() == nil {
			t.Error("cleanup received an active context after the overall deadline")
		}
		return nil
	}))
	signalTestShutdown(t, syscall.SIGTERM, started)
	waitForTestSignal(t, done, "overall shutdown deadline during probe drain")
	select {
	case <-cleaned:
		t.Error("shutdown waited for probe drain beyond its grace period")
	default:
	}
	// The existing drain sleep finishes in the background after shutdown returns.
	waitForTestSignal(t, cleaned, "completion of the background probe drain")
}

func TestShutdownWithoutComponents(t *testing.T) {
	if runShutdownSubprocess(t, 0) {
		return
	}
	for _, closers := range [][]ShutMeDown{nil, {nil, nil}} {
		ctx, cancel := context.WithCancel(t.Context())
		started, done := startTestShutdown(shutdownTestConfig(), cancel, closers...)
		signalTestShutdown(t, syscall.SIGTERM, started)
		waitForTestSignal(t, done, "shutdown without components")
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Errorf("application context = %v, want cancellation", ctx.Err())
		}
	}
}

func TestShutdownSecondSignalTerminatesProcess(t *testing.T) {
	if runShutdownSubprocess(t, syscall.SIGTERM) {
		return
	}
	cfg := shutdownTestConfig()
	cfg.Core.ShutdownGracePeriod = time.Hour
	cfg.Core.ShutdownComponentTimeout = time.Hour
	entered := make(chan struct{})
	started, done := startTestShutdown(cfg, func() {}, shutdownTestFunc(func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}))
	signalTestShutdown(t, syscall.SIGTERM, started)
	waitForTestSignal(t, entered, "graceful shutdown before second signal")
	if _, err := fmt.Fprintln(os.Stdout, shutdownSecondSignalMarker); err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, done, "second signal to terminate the process")
	t.Fatal("process survived the second shutdown signal")
}

func TestShutMeDownWrapDispatchesCleanup(t *testing.T) {
	failure := errors.New("cleanup failed")
	tests := []shutdownWrapTestCase{
		{name: "empty"},
		{name: "closer", withCloser: true},
		{name: "failing_closer", withCloser: true, failure: failure},
		{name: "function", withFunc: true},
		{name: "failing_function", withFunc: true, failure: failure},
		{name: "closer_precedence", withCloser: true, withFunc: true},
		{name: "failing_closer_precedence", withCloser: true, withFunc: true, failure: failure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			var closerCalls, functionCalls int
			component := &ShutMeDownWrap{}
			if test.withCloser {
				component.closer = shutdownTestCloser(func() error {
					closerCalls++
					return test.failure
				})
			}
			if test.withFunc {
				component.fn = func(cleanupCtx context.Context) error {
					functionCalls++
					if cleanupCtx != ctx {
						t.Error("cleanup function did not receive the caller's context")
					}
					return test.failure
				}
			}

			if err := component.Shutdown(ctx); !errors.Is(err, test.failure) {
				t.Errorf("Shutdown() error = %v, want %v", err, test.failure)
			}
			var wantCloserCalls, wantFunctionCalls int
			if test.withCloser {
				wantCloserCalls = 1
			} else if test.withFunc {
				wantFunctionCalls = 1
			}
			if closerCalls != wantCloserCalls || functionCalls != wantFunctionCalls {
				t.Errorf("cleanup calls = (%d, %d), want (%d, %d)",
					closerCalls, functionCalls, wantCloserCalls, wantFunctionCalls)
			}
		})
	}
}

func TestShutMeDownWrapBoundsUnresponsiveCleanup(t *testing.T) {
	for _, cleanup := range []string{"closer", "function"} {
		for _, stop := range []string{"cancellation", "deadline"} {
			t.Run(cleanup+"_"+stop, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var ctx context.Context
					var cancel context.CancelFunc
					if stop == "deadline" {
						ctx, cancel = context.WithTimeout(t.Context(), time.Second)
					} else {
						ctx, cancel = context.WithCancel(t.Context())
					}
					defer cancel()
					entered := make(chan struct{})
					release := make(chan struct{})
					finished := make(chan struct{})
					defer func() {
						close(release)
						synctest.Wait()
					}()
					block := func() error {
						defer close(finished)
						close(entered)
						<-release
						return errors.New("late cleanup failure")
					}
					component := &ShutMeDownWrap{}
					if cleanup == "closer" {
						component.closer = shutdownTestCloser(block)
					} else {
						component.fn = func(cleanupCtx context.Context) error {
							if cleanupCtx != ctx {
								t.Error("cleanup function did not receive the caller's context")
							}
							return block()
						}
					}
					result := make(chan error, 1)
					began := time.Now()
					go func() { result <- component.Shutdown(ctx) }()
					<-entered
					if stop == "deadline" {
						time.Sleep(time.Second)
					} else {
						cancel()
					}

					if err := <-result; err == nil || err.Error() != "timeout" {
						t.Errorf("Shutdown() error = %v, want timeout", err)
					}
					if stop == "deadline" && time.Since(began) != time.Second {
						t.Errorf("Shutdown() took %s, want the one-second deadline", time.Since(began))
					}
					select {
					case <-finished:
						t.Error("blocked cleanup completed before release")
					default:
					}
				})
			})
		}
	}
}

func shutdownTestConfig() *config.Config {
	return &config.Config{Core: config.Core{
		ShutdownGracePeriod:      2 * time.Second,
		ShutdownComponentTimeout: time.Second,
	}}
}

func startTestShutdown(
	cfg *config.Config, cancel context.CancelFunc, closers ...ShutMeDown,
) (<-chan struct{}, <-chan struct{}) {
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		shutdown(cfg, func() {
			cancel()
			close(started)
		}, closers...)
	}()
	return started, done
}

// Keep an extra signal receiver until shutdown acknowledges the signal. This
// avoids terminating the child if it has not reached signal.Notify yet.
func signalTestShutdown(t *testing.T, sig syscall.Signal, started <-chan struct{}) {
	t.Helper()
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, sig)
	defer signal.Stop(guard)
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Release() }()
	timeout := time.NewTimer(appTestTimeout)
	defer timeout.Stop()
	retry := time.NewTicker(time.Millisecond)
	defer retry.Stop()
	for {
		select {
		case <-started:
			return
		default:
		}
		if err := process.Signal(sig); err != nil {
			t.Fatalf("send shutdown signal: %v", err)
		}
		// Consume each delivery before stopping the guard or sending again.
		select {
		case <-guard:
		case <-timeout.C:
			t.Fatal("shutdown signal was not delivered")
		}
		select {
		case <-started:
			return
		case <-retry.C:
		case <-timeout.C:
			t.Fatal("shutdown did not acknowledge the signal")
		}
	}
}

// Return true in the parent after running this test in an isolated process.
func runShutdownSubprocess(t *testing.T, wantSignal syscall.Signal) bool {
	t.Helper()
	if os.Getenv(shutdownSubprocessEnv) == t.Name() {
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), shutdownSubprocessTimeout)
	defer cancel()
	args := []string{"-test.run=^" + regexp.QuoteMeta(t.Name()) + "$", "-test.v", "-test.timeout=10s"}
	if coverDir := flag.Lookup("test.gocoverdir"); coverDir != nil && len(coverDir.Value.String()) != 0 {
		args = append(args, "-test.gocoverdir="+coverDir.Value.String())
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = append(os.Environ(), shutdownSubprocessEnv+"="+t.Name())
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("shutdown subprocess timed out: %v\n%s", ctx.Err(), output)
	}
	if wantSignal == 0 {
		if err != nil {
			t.Fatalf("shutdown subprocess failed: %v\n%s", err, output)
		}
		return true
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("subprocess error = %v, want termination by %s\n%s", err, wantSignal, output)
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != wantSignal {
		t.Fatalf("subprocess status = %v, want signal %s\n%s", exitError.Sys(), wantSignal, output)
	}
	if !strings.Contains(string(output), shutdownSecondSignalMarker) {
		t.Fatalf("subprocess exited before reaching the second signal: %s", output)
	}
	return true
}

func waitForTestSignal(t *testing.T, done <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(appTestTimeout):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func assertReadinessWatcherRunning(t *testing.T, done <-chan struct{}, readinessCtx context.Context) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("readiness watcher stopped with an active channel")
	default:
	}
	if err := context.Cause(readinessCtx); err != nil {
		t.Fatalf("readiness failed without a component error: %v", err)
	}
}

func startReadinessWatcher(
	ctx context.Context, markUnready context.CancelCauseFunc, observables ...<-chan error,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchReadiness(ctx, markUnready, observables...)
	}()
	return done
}

func waitForReadinessWatcher(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(appTestTimeout):
		t.Fatal("readiness watcher did not stop")
	}
}
