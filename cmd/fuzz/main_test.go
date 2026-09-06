package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
)

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	module := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(module, []byte("module example.test/fuzz\n\ngo 1.27.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDiscoverTargets(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	otherOS := "windows"
	if runtime.GOOS == otherOS {
		otherOS = "linux"
	}
	dir := writeModule(t, map[string]string{
		"fixture.go": "package fixture\nfunc FuzzNonTest() {}\n",
		"fixture_test.go": `package fixture
import "testing"
func Fuzz(f *testing.F) {}
func FuzzAlpha(f *testing.F) { notDefined() }
func FuzzAlphaExtended(f *testing.F) {}
func FuzzBadSignature() {}
func FuzzÉ(f *testing.F) {}
func Fuzzer() {}
func Fuzzé() {}
type helper struct{}
func (helper) FuzzMethod(f *testing.F) {}
// func FuzzComment(f *testing.F) {}
var source = "func FuzzString(f *testing.F) {}"
`,
		"external_test.go":              "package fixture_test\nimport t \"testing\"\nfunc FuzzExternal(f *t.F) {}\n",
		"nested/fuzz_test.go":           "package nested\nimport \"testing\"\nfunc FuzzAlpha(f *testing.F) {}\n",
		"tagged_test.go":                "//go:build fuzzrunner_fixture\n\npackage fixture\nfunc FuzzTagged() {}\n",
		"other_" + otherOS + "_test.go": "package fixture\nfunc FuzzOtherOS() {}\n",
	})

	// Undefined functions and invalid signatures prove discovery does not compile tests.
	got, err := discoverTargets(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []target{
		{Package: "example.test/fuzz", Name: "Fuzz"},
		{Package: "example.test/fuzz", Name: "FuzzAlpha"},
		{Package: "example.test/fuzz", Name: "FuzzAlphaExtended"},
		{Package: "example.test/fuzz", Name: "FuzzBadSignature"},
		{Package: "example.test/fuzz", Name: "FuzzExternal"},
		{Package: "example.test/fuzz", Name: "FuzzÉ"},
		{Package: "example.test/fuzz/nested", Name: "FuzzAlpha"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("discovered %v, want %v", got, want)
	}

	t.Setenv("GOFLAGS", "-tags=fuzzrunner_fixture")
	got, err = discoverTargets(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want)+1 || !slices.Contains(got, target{Package: "example.test/fuzz", Name: "FuzzTagged"}) {
		t.Fatalf("build tag did not select the additional target: %v", got)
	}
}

func TestDiscoverTargetsErrors(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	for name, files := range map[string]map[string]string{
		"invalid module": {"go.mod": "invalid module file\n", "fixture.go": "package fixture\n"},
		"invalid test":   {"fixture_test.go": "package fixture\nfunc FuzzBroken(\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if targets, err := discoverTargets(t.Context(), writeModule(t, files)); err == nil {
				t.Fatalf("discovery succeeded with invalid input: %v", targets)
			}
		})
	}
}

func TestDiscoverTargetsEmpty(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	dir := writeModule(t, map[string]string{"fixture.go": "package fixture\n"})
	targets, err := discoverTargets(t.Context(), dir)
	if err != nil || len(targets) != 0 {
		t.Fatalf("empty discovery returned %v, %v", targets, err)
	}
}

func TestRunTargetsConcurrencyAndFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		targets := []target{
			{Package: "first", Name: "FuzzShared"},
			{Package: "second", Name: "FuzzShared"},
			{Package: "third", Name: "FuzzOther"},
			{Package: "fourth", Name: "FuzzOther"},
		}
		started := make(chan target, len(targets))
		release := make(chan struct{})
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runTargets(ctx, targets, func(ctx context.Context, target target) ([]byte, error) {
				started <- target
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
				}
				if target.Package == "first" {
					return []byte("failing output"), errors.New("target failed")
				}
				return []byte("passing output"), nil
			}, &out, true)
		}()
		synctest.Wait()
		if len(started) != 2 {
			t.Fatalf("started %d targets before either completed, want 2", len(started))
		}
		close(release)
		synctest.Wait()
		err := <-done
		if err == nil || !strings.Contains(err.Error(), "1 of 4 fuzz targets failed") {
			t.Fatalf("failure was not reported: %v", err)
		}
		if len(started) != len(targets) {
			t.Fatalf("only %d of %d targets ran", len(started), len(targets))
		}
		groups := 0
		open := false
		for line := range strings.SplitSeq(out.String(), "\n") {
			switch {
			case strings.HasPrefix(line, "::group::"):
				if open {
					t.Fatal("target log groups overlap")
				}
				open = true
				groups++
			case line == "::endgroup::":
				if !open {
					t.Fatal("log group ended without a beginning")
				}
				open = false
			}
		}
		if open || groups != len(targets) {
			t.Fatalf("incomplete target log groups: %s", out.String())
		}
	})
}

func TestRunTargetsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := make(chan target, 3)
		done := make(chan error, 1)
		go func() {
			done <- runTargets(ctx, make([]target, 3), func(ctx context.Context, target target) ([]byte, error) {
				started <- target
				<-ctx.Done()
				return nil, ctx.Err()
			}, io.Discard, false)
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation returned %v", err)
		}
		if len(started) != 2 {
			t.Fatalf("started %d targets; queued work should stay canceled", len(started))
		}
	})
}

func TestRunTargetsEmpty(t *testing.T) {
	var out bytes.Buffer
	err := runTargets(t.Context(), nil, nil, &out, false)
	if err != nil || !strings.Contains(out.String(), "No fuzz targets") {
		t.Fatalf("empty run returned %v, output %q", err, out.String())
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestRunTargetsOutputError(t *testing.T) {
	err := runTargets(t.Context(), []target{{Name: "FuzzExample"}}, nil, brokenWriter{}, false)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure returned %v", err)
	}
}
