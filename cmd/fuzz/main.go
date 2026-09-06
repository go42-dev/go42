package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const concurrentTargets = 2

type target struct {
	Package string
	Name    string
}

func main() {
	fuzzTime := flag.String("fuzztime", "30s", "Fuzzing duration or iteration count per target")
	list := flag.Bool("list", false, "List fuzz targets without running them")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	targets, err := discoverTargets(ctx, ".")
	if err == nil {
		if *list {
			for _, t := range targets {
				fmt.Printf("%s\t%s\n", t.Package, t.Name)
			}
			return
		}
		err = runTargets(ctx, targets, func(ctx context.Context, t target) ([]byte, error) {
			// #nosec G204 -- Values are separate Go arguments; no shell interprets them.
			cmd := exec.CommandContext(ctx, "go", "test", "-run", "^$", "-fuzz", "^"+t.Name+"$",
				"-fuzztime", *fuzzTime, "-parallel", "1", t.Package)
			cmd.WaitDelay = 5 * time.Second
			return cmd.CombinedOutput()
		}, os.Stdout, os.Getenv("GITHUB_ACTIONS") == "true")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func discoverTargets(ctx context.Context, dir string) ([]target, error) {
	// Only package metadata is needed; resolving dependencies would add unnecessary work.
	cmd := exec.CommandContext(ctx, "go", "list", "-find",
		"-json=Dir,ImportPath,TestGoFiles,XTestGoFiles", "./...")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("discover fuzz packages: %w\n%s", err, stderr.String())
	}

	var targets []target
	decoder := json.NewDecoder(bytes.NewReader(data))
	fset := token.NewFileSet()
	for {
		var pkg struct {
			Dir          string
			ImportPath   string
			TestGoFiles  []string
			XTestGoFiles []string
		}
		if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode fuzz package: %w", err)
		}
		for _, name := range append(pkg.TestGoFiles, pkg.XTestGoFiles...) {
			path := filepath.Join(pkg.Dir, name)
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return nil, fmt.Errorf("parse fuzz targets in %s: %w", path, err)
			}
			for _, declaration := range file.Decls {
				fn, ok := declaration.(*ast.FuncDecl)
				if ok && fn.Recv == nil && isFuzzName(fn.Name.Name) {
					// Let go test report invalid signatures instead of silently excluding them.
					targets = append(targets, target{Package: pkg.ImportPath, Name: fn.Name.Name})
				}
			}
		}
	}
	slices.SortFunc(targets, func(a, b target) int {
		return cmp.Or(cmp.Compare(a.Package, b.Package), cmp.Compare(a.Name, b.Name))
	})
	return targets, nil
}

func isFuzzName(name string) bool {
	suffix, ok := strings.CutPrefix(name, "Fuzz")
	if !ok {
		return false
	}
	// Match Go's test naming rule, including Unicode and the bare name Fuzz.
	r, _ := utf8.DecodeRuneInString(suffix)
	return suffix == "" || !unicode.IsLower(r)
}

func runTargets(
	ctx context.Context,
	targets []target,
	execute func(context.Context, target) ([]byte, error),
	out io.Writer,
	githubActions bool,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var writeErr error
	print := func(format string, args ...any) {
		if writeErr != nil {
			return
		}
		if _, err := fmt.Fprintf(out, format, args...); err != nil {
			writeErr = fmt.Errorf("write fuzz output: %w", err)
			cancel()
		}
	}
	if len(targets) == 0 {
		print("No fuzz targets found.\n")
		return writeErr
	}
	print("Found %d fuzz targets; running up to %d concurrently.\n", len(targets), concurrentTargets)

	jobs := make(chan target, len(targets))
	for _, t := range targets {
		jobs <- t
	}
	close(jobs)
	type result struct {
		target  target
		started bool
		output  []byte
		err     error
	}
	results := make(chan result)
	var workers sync.WaitGroup
	for range min(concurrentTargets, len(targets)) {
		workers.Go(func() {
			for t := range jobs {
				if ctx.Err() != nil {
					return
				}
				results <- result{target: t, started: true}
				output, err := execute(ctx, t)
				results <- result{target: t, output: output, err: err}
			}
		})
	}
	go func() {
		workers.Wait()
		close(results)
	}()

	failed := 0
	for r := range results {
		if r.started {
			print("Starting %s / %s\n", r.target.Package, r.target.Name)
			continue
		}
		status := "PASS"
		if r.err != nil {
			status = "FAIL"
			failed++
		}
		if githubActions {
			print("::group::")
		}
		print("%s: %s / %s\n", status, r.target.Package, r.target.Name)
		print("%s", r.output)
		if len(r.output) > 0 && r.output[len(r.output)-1] != '\n' {
			print("\n")
		}
		if r.err != nil {
			print("Error: %v\n", r.err)
		}
		if githubActions {
			print("::endgroup::\n")
		}
	}
	if writeErr != nil {
		return writeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d fuzz targets failed", failed, len(targets))
	}
	print("All %d fuzz targets passed.\n", len(targets))
	return writeErr
}
