# Go Code Style

## Project defaults

Follow explicit user instructions and project conventions before these defaults
or upstream recommendations. Use the project's prescribed formatters and checks.
These defaults apply to handwritten Go. Generated code follows its generator's
conventions.

### Comments

- Start declaration documentation comments with the declared identifier and a
  complete descriptive sentence. Put supplementary tags such as `@see`, `@note`,
  `@todo`, and `@fixme` directly on following lines, without a blank comment line.
- Use comment tags such as `@see`, `@todo`, `@fixme`, and `@note` where useful.
- Use `@see` for links to related documentation, articles, or blog posts.
- Use `// ---` comments to separate sections in code files.

Declaration comments describe the symbol before adding supplementary tags:

```go
// MCPServer describes an MCP server in Claude's configuration.
// @see https://docs.anthropic.com/en/docs/claude-code/mcp
type MCPServer struct {
    Command string `json:"command,omitempty"`
}
```

Tags explain constraints, link supporting documentation, or record outstanding
work. For example:

```go
// @note Reconnect attempts must observe cancellation.
// @see https://go.dev/wiki/CodeReviewComments#contexts

// @todo Add a cancellation test for reconnect attempts.
// @fixme Ensure reconnect attempts respect the shutdown deadline.
```

### Formatting

- Write function bodies and control flow on multiple lines, including functions
  that only return a value. Avoid one-line functions.
- Write map literals with multiple entries and struct literals with multiple
  fields across multiple lines, including in loop headers, arguments, and return
  statements. Put each entry or field on its own line, use a trailing comma, and
  put the closing brace on a separate line. Empty and single-entry or single-field
  literals may stay on one line.
- End text files with a newline.

Keep map entries and struct fields on separate lines, including a struct literal
passed directly to a function. This example assumes a named `Settings` type with
`Timeout` and `Attempts` fields:

```go
delays := map[int]time.Duration{
    1: time.Second,
    2: 2 * time.Second,
}

configure(Settings{
    Timeout:  5 * time.Second,
    Attempts: len(delays),
})
```

Single-entry and single-field literals may remain compact, such as
`map[int]time.Duration{1: time.Second}` and `Settings{Timeout: time.Second}`.

### Readability

- Use blank lines to separate logical steps, such as loading, validation, and
  execution. Keep related statements together: a call with its error check,
  resource acquisition with cleanup registration, and assignments that build one
  value. Follow the surrounding grouping when extending existing code.
- Group struct fields by responsibility, such as dependencies, configuration,
  and runtime state. Separate those groups with blank lines.

Separate logical steps, keeping each call with its error check:

```go
settings, err := loadSettings(path)
if err != nil {
    return err
}

if err := settings.Validate(); err != nil {
    return err
}

return run(ctx, settings)
```

Keep resource acquisition and cleanup registration together:

```go
ctx, cancel := context.WithTimeout(ctx, timeout)
defer cancel()

return client.Fetch(ctx)
```

Within loops, separate filtering from processing:

```go
for _, entry := range entries {
    if entry.IsDir() {
        continue
    }

    names = append(names, entry.Name())
}
```

In tests, separate setup, execution, and assertions:

```go
input := []int{3, 1, 2}
want := []int{1, 2, 3}

slices.Sort(input)

if !slices.Equal(input, want) {
    t.Fatalf("got %v, want %v", input, want)
}
```

Group dependencies, configuration, and runtime state:

```go
type Service struct {
    logger     *slog.Logger
    repository repository

    timeout   time.Duration
    batchSize int

    mu      sync.Mutex
    running bool
}
```

### Naming and declarations

| Convention          | Guidance                                               |
| ------------------- | ------------------------------------------------------ |
| Generic values      | Prefer `any` over `interface{}`.                       |
| Context variables   | Name `context.Context` variables `ctx`.                |
| Empty string checks | Prefer `len(s) == 0` over `s == ""` for empty strings. |
| YAML filenames      | Prefer `.yaml` over `.yml`.                            |

- Use named structs and interfaces. Anonymous structs and interfaces, including
  type assertions to anonymous interfaces, are allowed only in tests.
- Define types and constants at package scope. Function-local type and constant
  declarations are allowed only in tests.
- Name intentionally unused parameters `_` in function and method
  implementations, including parameters retained for a common constructor
  signature. Keep descriptive parameter names in interface declarations.

Keep common constructor signatures explicit even when some parameters are unused:

```go
func New(
    _ *slog.Logger,
    settings Settings,
    _ provider.TemplateEngineAccessor,
    _ string,
) *Provider {
    return &Provider{settings: settings}
}
```

Reuse standard interfaces such as `io.Closer` when they describe the capability.
For a custom capability, declare the interface once and use its name in type
assertions:

```go
type cacheAccessor interface {
    Invalidate(ctx context.Context, key string) error
}

func Invalidate(ctx context.Context, value any, key string) error {
    cache, ok := value.(cacheAccessor)
    if !ok {
        return errors.New("value does not support cache invalidation")
    }

    return cache.Invalidate(ctx, key)
}
```

The table-driven example under [Testing](#testing) demonstrates a function-local
constant and an anonymous struct, both permitted in tests.

### String constants

- Use named string constants for values with shared or stable meaning, such as
  protocol values, configuration keys, or values reused across operations.
- A single-use value can warrant a constant when its name explains that meaning;
  ordinary messages and disposable test data can remain literals.
- Reuse constants from the package that owns the value. Group constants after
  imports and before other declarations. Apply the same judgment in tests.

Name protocol values even when they appear in only one operation. Keep ordinary
error messages inline:

```go
const (
    headerContentType = "Content-Type"
    contentTypeJSON   = "application/json"
)

func WriteJSON(w http.ResponseWriter, value any) error {
    w.Header().Set(headerContentType, contentTypeJSON)

    if err := json.NewEncoder(w).Encode(value); err != nil {
        return fmt.Errorf("encode response: %w", err)
    }

    return nil
}
```

For values already defined by another package, use its constant, such as
`http.MethodGet`, instead of defining a second constant for `"GET"`.

### Context values

- Use private, named key types for context values rather than plain string keys.
- Keep keys and access functions in the package that owns the value. Callers use
  those functions instead of repeating `ctx.Value` or `context.WithValue`.
- Make the accessor's behavior for a missing value clear.

For request identity, the accessor returns an empty string when no ID is present:

```go
const requestIDContextKey contextKey = "request_id"

type contextKey string

func WithRequestID(ctx context.Context, requestID string) context.Context {
    return context.WithValue(ctx, requestIDContextKey, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
    requestID, _ := ctx.Value(requestIDContextKey).(string)
    return requestID
}
```

### Errors

- Wrap errors with `fmt.Errorf` and `%w` when callers need the underlying error.
- Inspect errors with `errors.Is` and `errors.As`, matching identity or type
  rather than message text.
- Return errors from services and libraries. Command entry points decide the
  process exit status.
- Start ordinary error messages with lowercase text and omit trailing
  punctuation. Preserve capitalization for proper names and acronyms.

Add operation context while keeping the error suitable for wrapping:

```go
return fmt.Errorf("load configuration %q: %w", path, err)
```

Inspect a wrapped cause with `errors.Is`. Use `errors.As` when the error's type
provides details, such as the path of a failed file operation:

```go
if errors.Is(err, fs.ErrNotExist) {
    return fmt.Errorf("configuration is missing: %w", err)
}

var pathErr *fs.PathError
if errors.As(err, &pathErr) {
    return fmt.Errorf("load configuration %q: %w", pathErr.Path, err)
}

return err
```

### Dependency interfaces and boundaries

- Define dependency interfaces in the consuming package with only the methods
  that consumer needs. Keep them beside the consumer or group them in `accessors.go`.
- Prefer names such as `xxxAccessor`, or a simple subsystem name such as `cache`
  or `events`.
- Keep mock-generation directives with the interface declarations and generate
  mocks into the package's `mocks/` directory.
- Entry points compose dependencies and own process lifecycle. Services own
  business operations. Adapters own protocol parsing, conversion, and error
  mapping. Follow the project's existing package layout.
- Parse and validate settings before initializing dependencies.

In the consuming package's `accessors.go`, declare only the operation the
service needs and keep the mock-generation directive beside it:

```go
//go:generate mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type repository interface {
    Lookup(ctx context.Context, key string) (string, error)
}
```

The constructor under [Functional options](#functional-options) accepts this
interface as a required dependency. A concrete repository can implement many
other operations without adding them to this interface.

### Resource ownership

- Give background goroutines and resources explicit owners and shutdown paths.
  Use request contexts for request work and component contexts for workers.
- Bound external calls and retry waits. Keep concurrency limits in force until
  the underlying work finishes, including after a caller times out.
- Shutdown must wait for owned work and release resources within its deadline.
  Report incomplete cleanup. Release acquired resources when initialization
  fails partway through.
- Expose receive-only channels to consumers. Keep sending and closing with the
  owner, and document when the channel closes.

The producer owns this buffered result channel. Consumers can receive the result
without being able to send or close it. The callback must observe cancellation
through the supplied context:

```go
// Start runs work and closes the channel after sending its result.
func Start(ctx context.Context, run func(context.Context) error) <-chan error {
    result := make(chan error, 1)

    go func() {
        defer close(result)
        result <- run(ctx)
    }()

    return result
}
```

Give an owned worker a cancellation function and a completion channel. The
callback observes its component context; shutdown cancels it and waits for it
to finish. The caller supplies a separate, deadline-bound shutdown context:

```go
type Signal struct{}

type Worker struct {
    cancel context.CancelFunc
    done   chan Signal
}

func NewWorker(ctx context.Context, run func(context.Context)) *Worker {
    ctx, cancel := context.WithCancel(ctx)
    w := &Worker{
        cancel: cancel,
        done:   make(chan Signal),
    }

    go func() {
        defer close(w.done)
        run(ctx)
    }()

    return w
}

func (w *Worker) Shutdown(ctx context.Context) error {
    w.cancel()

    select {
    case <-w.done:
        return nil
    case <-ctx.Done():
        return fmt.Errorf("wait for worker shutdown: %w", ctx.Err())
    }
}
```

When initialization fails after acquiring a connection, close it before
returning. Here, `dial` and `initialize` are project operations, and `logger` is
the component's injected logger. Report a cleanup failure as well:

```go
conn, err := dial(ctx)
if err != nil {
    return nil, fmt.Errorf("connect: %w", err)
}

if err := initialize(ctx, conn); err != nil {
    if closeErr := conn.Close(); closeErr != nil {
        logger.ErrorContext(ctx, "close connection after failed initialization",
            slog.Any("error", closeErr),
        )
    }

    return nil, fmt.Errorf("initialize connection: %w", err)
}

return conn, nil
```

For a concurrency limit, use a buffered slot channel owned by the component,
such as `make(chan Signal, 4)`. With a deadline-bound request context, waiting
can end before the underlying callback returns. Release the slot in the
goroutine that performs the work, rather than when the caller stops waiting:

```go
func runBounded(ctx context.Context, slots chan Signal, run func(context.Context) error) error {
    select {
    case slots <- Signal{}:
    case <-ctx.Done():
        return ctx.Err()
    }

    result := make(chan error, 1)

    go func() {
        defer func() {
            <-slots
        }()

        if err := ctx.Err(); err != nil {
            result <- err
            return
        }

        result <- run(ctx)
    }()

    select {
    case err := <-result:
        return err
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

### Logging

- Inject component loggers through options and default to a discard logger when
  none is supplied. Use a `component` field to identify the owner of a log.
- Prefer context-aware logging when a context is available.
- Use typed `slog.Attr` arguments and `snake_case` field names. Follow the
  project's lint rules for formatting attribute lists.
- With `slog`, use `slog.Any("error", err)` for logged errors.

The [functional-options example](#functional-options) supplies `WithLogger`,
defaults to a discard logger, and attaches the service's `component` field.

Keep field names consistent and preserve each value's type:

```go
logger.ErrorContext(ctx, "request failed",
    slog.String("request_id", requestID),
    slog.Duration("elapsed", elapsed),
    slog.Any("error", err),
)
```

### Testing

- Test observable behavior and failure paths.
- Keep unit tests for `foo.go` together in `foo_test.go` in the same directory,
  even when the suite grows. Use descriptive names for package-wide, integration,
  and fuzz tests.
- Put integration and end-to-end tests in `tests/integration/` and `tests/e2e/`.
  Keep other suites in their matching directories under `tests/` when used.
- Isolate resources, clean them up, and bound waits. Prefer synchronization or
  `testing/synctest` over fixed sleeps for in-process concurrency.
- Run the checks appropriate to the changed behavior and affected callers.
  Report checks actually performed, their results, and material gaps in
  verification.

Exercise a failure path and assert the observable result. Here,
`newTestRepository` supplies an isolated fixture implementing `repository`,
and `NewService` is the constructor shown under [Functional options](#functional-options):

```go
func TestNewServiceRejectsInvalidTimeout(t *testing.T) {
    const negativeTimeout = -time.Second

    repo := newTestRepository()
    tests := []struct {
        name    string
        timeout time.Duration
    }{
        {
            name:    "zero",
            timeout: 0,
        },
        {
            name:    "negative",
            timeout: negativeTimeout,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            service, err := NewService(repo, WithTimeout(tt.timeout))
            if err == nil || service != nil {
                t.Fatalf("NewService = (%v, %v), want nil service and an error", service, err)
            }
        })
    }
}
```

For concurrent work, synchronize on observable events. This test uses the
worker above and `testing/synctest` to establish that shutdown is still waiting
while the callback is blocked, then releases it without a fixed sleep:

```go
func TestWorkerShutdownWaitsForWork(t *testing.T) {
    synctest.Test(t, func(t *testing.T) {
        release := make(chan Signal)
        worker := NewWorker(t.Context(), func(ctx context.Context) {
            <-ctx.Done()
            <-release
        })

        result := make(chan error, 1)
        go func() {
            ctx, cancel := context.WithTimeout(t.Context(), time.Second)
            defer cancel()

            result <- worker.Shutdown(ctx)
        }()

        synctest.Wait()
        select {
        case err := <-result:
            close(release)
            t.Fatalf("shutdown returned before work finished: %v", err)
        default:
        }

        close(release)
        if err := <-result; err != nil {
            t.Fatalf("shutdown: %v", err)
        }
    })
}
```

### Generated and vendored files

- Edit source definitions and generator configuration, then regenerate outputs
  through the project's workflow.
- Update vendored files through their dependency update process, preserving
  recorded versions and sources.

## Patterns

### Functional options

- When a component has optional settings, use named functional options such as
  `Option` and `WithTimeout`. Keep required dependencies explicit in the
  constructor's parameters, with `opts ...Option` last.
- Keep option declarations and functions together in `options.go` when following
  the project's package layout. Options configure values; constructors own
  validation and fallible initialization.
- Set defaults, apply options in the supplied order, and validate the resulting
  configuration before opening connections or starting owned work.
- Keep constructors without optional settings simple; they do not need options.

This example uses the consumer-owned `repository` interface declared above.
Logger injection is another optional setting:

```go
const defaultTimeout = 5 * time.Second

type Service struct {
    logger     *slog.Logger
    repository repository

    timeout time.Duration
}

type Option func(*Service)

func WithLogger(logger *slog.Logger) Option {
    return func(s *Service) {
        s.logger = logger
    }
}

func WithTimeout(timeout time.Duration) Option {
    return func(s *Service) {
        s.timeout = timeout
    }
}

func NewService(repository repository, opts ...Option) (*Service, error) {
    s := &Service{
        repository: repository,
        timeout:    defaultTimeout,
    }

    for _, opt := range opts {
        opt(s)
    }

    if s.timeout <= 0 {
        return nil, errors.New("timeout must be positive")
    }

    if s.logger == nil {
        s.logger = slog.New(slog.DiscardHandler)
    }
    s.logger = s.logger.With(slog.String("component", "service"))

    return s, nil
}
```

Callers supply required dependencies directly and override optional settings:

```go
service, err := NewService(repo,
    WithTimeout(10*time.Second),
    WithLogger(logger),
)
if err != nil {
    return err
}
```

Calling `NewService(repo)` uses the default timeout.

## Read detailed conventions on demand

Before editing or reviewing Go code, consult the sections relevant to the task
using the table below. Paths are relative to the project root. These Markdown
snapshots are installed locally by go42x; no network access is needed.

When available, use the knowledge-base tools from the `go42x` MCP server. Search
with `kwb_search`, setting `kind="documentation"`, `language="md"`, and `path_prefix`
to the selected reference. Read matching sections with `kwb_get_file` using their
source line ranges. If the tools are unavailable or the reference is not indexed,
use `rg` (ripgrep) to find headings or symbols and read the relevant source lines
with a file-reading tool. Follow cross-references when more detail is needed.
Read only the necessary sections; do not load every reference into the initial
context.

| Task or question                                 | Local reference                               | Sections to find                                                          |
| ------------------------------------------------ | --------------------------------------------- | ------------------------------------------------------------------------- |
| Choosing package names and package boundaries    | `.go42x/upstream/go/package-names.md`         | Package names, Naming package contents, Package paths, Bad package names  |
| Naming exported APIs, getters, and interfaces    | `.go42x/upstream/go/effective-go.md`          | Package names, Getters, Interface names, MixedCaps                        |
| Common review issues: names, imports, APIs       | `.go42x/upstream/go/code-review-comments.md`  | Imports, Initialisms, Interfaces, Receiver Type, Contexts, Handle Errors  |
| Choosing a Google style document                 | `.go42x/upstream/google-go/index.md`          | About, Documents                                                          |
| Readability, simplicity, formatting, consistency | `.go42x/upstream/google-go/guide.md`          | Style principles, Core guidelines, Local consistency                      |
| Detailed rules for code, comments, errors, tests | `.go42x/upstream/google-go/decisions.md`      | Naming, Commentary, Imports, Errors, Language, Contexts, Test structure   |
| Practical examples and design tradeoffs          | `.go42x/upstream/google-go/best-practices.md` | Error handling, Documentation, Function argument lists, Tests, Interfaces |
