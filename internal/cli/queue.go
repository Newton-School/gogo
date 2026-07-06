package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	q "github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/backends"
	_ "github.com/Newton-School/gogo/queue/backends/redis"
	"github.com/Newton-School/gogo/queue/brokers"
	_ "github.com/Newton-School/gogo/queue/brokers/redis"
	_ "github.com/Newton-School/gogo/queue/schedulers/redis"
)

var defaultQueueRuntime = NewQueueRuntime()

type QueueRuntime struct {
	App         *q.App
	Broker      q.Broker
	Backend     q.ResultBackend
	Store       q.ScheduleStore
	Revocations *q.RevocationRegistry
	Events      *q.EventRecorder
	Workers     []*q.Worker
	Now         func() time.Time
}

func NewQueueRuntime() *QueueRuntime {
	now := time.Now
	return &QueueRuntime{
		App:         q.NewApp(q.AppOptions{}),
		Broker:      brokers.NewMemoryBroker(brokers.MemoryOptions{}),
		Backend:     backends.NewMemoryBackend(backends.MemoryOptions{}),
		Store:       q.NewMemoryScheduleStore(q.MemoryScheduleStoreOptions{Now: now}),
		Revocations: q.NewRevocationRegistry(),
		Events:      q.NewEventRecorder(),
		Now:         now,
	}
}

func (r *QueueRuntime) Inspector() *q.Inspector {
	return q.NewInspector(q.InspectOptions{
		App:         r.App,
		Broker:      r.Broker,
		Store:       r.Store,
		Workers:     r.Workers,
		Revocations: r.Revocations,
		Events:      r.Events,
	})
}

func NewWorkerCommand(runtime *QueueRuntime) Command {
	return queueWorkerCommand{runtime: runtime}
}

func NewBeatCommand(runtime *QueueRuntime) Command {
	return queueBeatCommand{runtime: runtime}
}

func NewInspectCommand(runtime *QueueRuntime) Command {
	return queueInspectCommand{runtime: runtime}
}

func NewQueuesCommand(runtime *QueueRuntime) Command {
	return queueQueuesCommand{runtime: runtime}
}

type queueWorkerCommand struct {
	runtime *QueueRuntime
}

func (c queueWorkerCommand) Name() string    { return "worker" }
func (c queueWorkerCommand) Summary() string { return "Run a queue worker" }
func (c queueWorkerCommand) Run(ctx context.Context, args []string) error {
	return c.runWithIO(ctx, args, io.Discard, io.Discard)
}

func (c queueWorkerCommand) runWithIO(ctx context.Context, args []string, stdout, _ io.Writer) error {
	runtime := c.runtime
	if runtime == nil {
		runtime = defaultQueueRuntime
	}
	options, err := parseWorkerFlags(args)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	if err := configureQueueRuntime(runtime, q.RuntimeConfig{BrokerURL: options.brokerURL, ResultBackend: options.resultBackend}); err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	if options.check {
		if err := checkQueueRuntimeReachable(ctx, runtime); err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
	}
	if err := options.applyTaskTimeLimits(runtime.App); err != nil {
		return err
	}
	worker := q.NewWorker(runtime.App, runtime.Broker, runtime.Backend, options.workerOptions(runtime))
	runtime.Workers = append(runtime.Workers, worker)
	if options.check {
		_, err := fmt.Fprintf(stdout, "worker %s configured queues=%s concurrency=%d prefetch=%d broker=%s backend=%s log=%s\n", options.hostname, strings.Join(options.queues, ","), options.concurrency, options.prefetchMultiplier, options.brokerURL, options.resultBackend, options.logLevel)
		return err
	}
	if options.once {
		err := worker.RunOnce(ctx)
		if errors.Is(err, q.ErrQueueEmpty) {
			_, _ = fmt.Fprintf(stdout, "worker %s found no tasks\n", options.hostname)
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
		_, err = fmt.Fprintf(stdout, "worker %s processed one task\n", options.hostname)
		return err
	}
	return worker.Run(ctx)
}

type workerCLIOptions struct {
	appSettings         string
	concurrency         int
	autoscale           q.AutoscaleConfig
	queues              []string
	hostname            string
	logLevel            string
	brokerURL           string
	resultBackend       string
	pool                string
	prefetchMultiplier  int
	maxTasksPerChild    int
	maxMemoryPerChild   uint64
	softTimeLimit       time.Duration
	hardTimeLimit       time.Duration
	gracefulTimeout     time.Duration
	acceptedSerializers []string
	once                bool
	check               bool
}

func parseWorkerFlags(args []string) (workerCLIOptions, error) {
	options := workerCLIOptions{
		concurrency:        1,
		queues:             []string{"default"},
		hostname:           "worker",
		logLevel:           "info",
		brokerURL:          "memory://",
		resultBackend:      "memory",
		pool:               string(q.PoolGoroutine),
		prefetchMultiplier: 1,
		gracefulTimeout:    30 * time.Second,
	}
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var autoscale string
	var queues string
	var accepted string
	flags.StringVar(&options.appSettings, "app-settings", "", "settings module")
	flags.IntVar(&options.concurrency, "concurrency", 1, "worker concurrency")
	flags.StringVar(&autoscale, "autoscale", "", "min,max autoscale concurrency")
	flags.StringVar(&queues, "queues", "default", "comma-separated queues")
	flags.StringVar(&options.hostname, "hostname", "worker", "worker hostname")
	flags.StringVar(&options.logLevel, "log-level", "info", "log level")
	flags.StringVar(&options.brokerURL, "broker-url", "memory://", "broker URL")
	flags.StringVar(&options.resultBackend, "result-backend", "memory", "result backend")
	flags.StringVar(&options.pool, "pool", string(q.PoolGoroutine), "pool strategy")
	flags.IntVar(&options.prefetchMultiplier, "prefetch-multiplier", 1, "prefetch multiplier")
	flags.IntVar(&options.maxTasksPerChild, "max-tasks-per-child", 0, "max tasks per child")
	flags.Uint64Var(&options.maxMemoryPerChild, "max-memory-per-child", 0, "max memory per child")
	flags.DurationVar(&options.softTimeLimit, "soft-time-limit", 0, "soft time limit")
	flags.DurationVar(&options.hardTimeLimit, "hard-time-limit", 0, "hard time limit")
	flags.DurationVar(&options.gracefulTimeout, "graceful-timeout", 30*time.Second, "graceful shutdown timeout")
	flags.StringVar(&accepted, "accepted-serializers", "application/json", "accepted serializer content types")
	flags.BoolVar(&options.once, "once", false, "process one task and exit")
	flags.BoolVar(&options.check, "check", false, "validate configuration and exit")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	options.queues = splitCSV(queues)
	options.acceptedSerializers = splitCSV(accepted)
	if autoscale != "" {
		minimum, maximum, err := parsePair(autoscale)
		if err != nil {
			return options, err
		}
		options.autoscale = q.AutoscaleConfig{MinConcurrency: minimum, MaxConcurrency: maximum}
	}
	allowlist := q.NewContentTypeAllowlist(options.acceptedSerializers...)
	for _, serializer := range options.acceptedSerializers {
		if err := allowlist.Validate(serializer); err != nil {
			return options, err
		}
	}
	return options, nil
}

func (o workerCLIOptions) workerOptions(runtime *QueueRuntime) q.WorkerOptions {
	return q.WorkerOptions{
		Hostname:                o.hostname,
		Queues:                  o.queues,
		Concurrency:             o.concurrency,
		PrefetchMultiplier:      o.prefetchMultiplier,
		ShutdownTimeout:         o.gracefulTimeout,
		Autoscale:               o.autoscale,
		Pool:                    poolFromName(o.pool),
		MaxTasksPerWorkerChild:  o.maxTasksPerChild,
		MaxMemoryPerWorkerChild: o.maxMemoryPerChild,
		Revocations:             runtime.Revocations,
		Events:                  runtime.Events,
	}
}

func (o workerCLIOptions) applyTaskTimeLimits(app *q.App) error {
	if app == nil || (o.softTimeLimit == 0 && o.hardTimeLimit == 0) {
		return nil
	}
	for _, task := range app.Tasks() {
		if err := app.SetTaskTimeLimit(task.Name, o.softTimeLimit, o.hardTimeLimit); err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
	}
	return nil
}

func configureQueueRuntime(runtime *QueueRuntime, config q.RuntimeConfig) error {
	if runtime == nil {
		return nil
	}
	if !isMemoryRuntimeURL(config.BrokerURL) {
		broker, err := q.NewBrokerFromURL(config)
		if err != nil {
			return err
		}
		runtime.Broker = broker
	}
	if !isMemoryRuntimeURL(config.ResultBackend) {
		backend, err := q.NewResultBackendFromURL(config)
		if err != nil {
			return err
		}
		runtime.Backend = backend
	}
	if !isMemoryRuntimeURL(config.ScheduleStore) {
		store, err := q.NewScheduleStoreFromURL(config)
		if err != nil {
			return err
		}
		runtime.Store = store
	}
	return nil
}

type queueRuntimePinger interface {
	Ping(context.Context) error
}

func checkQueueRuntimeReachable(ctx context.Context, runtime *QueueRuntime) error {
	if runtime == nil {
		return nil
	}
	if pinger, ok := runtime.Broker.(queueRuntimePinger); ok {
		if err := pinger.Ping(ctx); err != nil {
			return fmt.Errorf("Redis broker is not reachable: %w", err)
		}
	}
	if pinger, ok := runtime.Backend.(queueRuntimePinger); ok {
		if err := pinger.Ping(ctx); err != nil {
			return fmt.Errorf("Redis result backend is not reachable: %w", err)
		}
	}
	return nil
}

func isMemoryRuntimeURL(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized == "" || normalized == "memory" || strings.HasPrefix(normalized, "memory://")
}

type queueBeatCommand struct {
	runtime *QueueRuntime
}

func (c queueBeatCommand) Name() string    { return "beat" }
func (c queueBeatCommand) Summary() string { return "Run the queue scheduler" }
func (c queueBeatCommand) Run(ctx context.Context, args []string) error {
	return c.runWithIO(ctx, args, io.Discard, io.Discard)
}

func (c queueBeatCommand) runWithIO(ctx context.Context, args []string, stdout, _ io.Writer) error {
	runtime := c.runtime
	if runtime == nil {
		runtime = defaultQueueRuntime
	}
	flags := flag.NewFlagSet("beat", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	once := flags.Bool("once", false, "run one scheduler tick")
	interval := flags.Duration("interval", time.Second, "beat loop interval")
	schedulePath := flags.String("schedule-path", "memory://", "schedule store path")
	brokerURL := flags.String("broker-url", "memory://", "broker URL")
	appSettings := flags.String("app-settings", "", "settings module")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	_ = appSettings
	if err := configureQueueRuntime(runtime, q.RuntimeConfig{BrokerURL: *brokerURL, ScheduleStore: *schedulePath}); err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	beat := q.NewBeat(runtime.App, runtime.Broker, runtime.Store, q.BeatOptions{Now: runtime.Now})
	if *once {
		enqueued, err := beat.Tick(ctx)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
		_, err = fmt.Fprintf(stdout, "beat enqueued %d task(s)\n", enqueued)
		return err
	}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		if _, err := beat.Tick(ctx); err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type queueInspectCommand struct {
	runtime *QueueRuntime
}

func (c queueInspectCommand) Name() string    { return "inspect" }
func (c queueInspectCommand) Summary() string { return "Inspect queue workers" }
func (c queueInspectCommand) Run(ctx context.Context, args []string) error {
	return c.runWithIO(ctx, args, io.Discard, io.Discard)
}

func (c queueInspectCommand) runWithIO(ctx context.Context, args []string, stdout, _ io.Writer) error {
	runtime := c.runtime
	if runtime == nil {
		runtime = defaultQueueRuntime
	}
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reportFlag := flags.Bool("report", false, "print report")
	pingFlag := flags.Bool("ping", false, "ping workers")
	revoke := flags.String("revoke", "", "revoke task ID")
	revokeStamped := flags.String("revoke-stamped", "", "revoke stamped header key=value")
	enableEvents := flags.Bool("enable-events", false, "enable events")
	disableEvents := flags.Bool("disable-events", false, "disable events")
	rateLimit := flags.String("rate-limit", "", "task=limit/period")
	timeLimit := flags.String("time-limit", "", "task=soft,hard")
	poolGrow := flags.Int("pool-grow", 0, "grow first worker pool")
	poolShrink := flags.Int("pool-shrink", 0, "shrink first worker pool")
	shutdown := flags.Bool("shutdown", false, "shutdown first worker")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	inspector := runtime.Inspector()
	if *enableEvents {
		inspector.EnableEvents()
	}
	if *disableEvents {
		inspector.DisableEvents()
	}
	if *revoke != "" {
		inspector.RevokeTask(*revoke)
	}
	if *revokeStamped != "" {
		key, value, err := parseKeyValue(*revokeStamped)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
		inspector.RevokeByStampedHeaders(key, value)
	}
	if *rateLimit != "" {
		task, limit, err := parseRateLimit(*rateLimit)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
		if err := inspector.RateLimit(task, limit); err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
	}
	if *timeLimit != "" {
		task, soft, hard, err := parseTimeLimit(*timeLimit)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
		if err := inspector.TimeLimit(task, soft, hard); err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
	}
	firstWorker := firstWorker(runtime)
	if *poolGrow > 0 {
		inspector.PoolGrow(firstWorker, *poolGrow)
	}
	if *poolShrink > 0 {
		inspector.PoolShrink(firstWorker, *poolShrink)
	}
	if *shutdown {
		if err := inspector.Shutdown(ctx, firstWorker, q.GracefulShutdown); err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
	}
	if *pingFlag {
		pong := inspector.Ping(ctx)
		_, _ = fmt.Fprintf(stdout, "pong hostname=%s ok=%t\n", pong.Hostname, pong.OK)
	}
	if *reportFlag || !*pingFlag {
		report, err := inspector.Report(ctx)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
		_, _ = fmt.Fprintf(stdout, "registered=%d active=%d scheduled=%d reserved=%d queues=%d workers=%d\n", len(report.Registered), len(report.Active), len(report.Scheduled), len(report.Reserved), len(report.Queues), len(report.Workers))
	}
	return nil
}

type queueQueuesCommand struct {
	runtime *QueueRuntime
}

func (c queueQueuesCommand) Name() string    { return "queues" }
func (c queueQueuesCommand) Summary() string { return "Inspect queues" }
func (c queueQueuesCommand) Run(ctx context.Context, args []string) error {
	return c.runWithIO(ctx, args, io.Discard, io.Discard)
}

func (c queueQueuesCommand) runWithIO(ctx context.Context, args []string, stdout, _ io.Writer) error {
	runtime := c.runtime
	if runtime == nil {
		runtime = defaultQueueRuntime
	}
	flags := flag.NewFlagSet("queues", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	queueName := flags.String("queue", "", "queue name")
	purge := flags.Bool("purge", false, "purge queue")
	brokerURL := flags.String("broker-url", "memory://", "broker URL")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	_ = brokerURL
	if *purge {
		name := *queueName
		if name == "" {
			name = "default"
		}
		count, err := runtime.Broker.PurgeQueue(ctx, name)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrCommandFailed, err)
		}
		_, err = fmt.Fprintf(stdout, "%s purged=%d\n", name, count)
		return err
	}
	queues, err := runtime.Broker.InspectQueues(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	printed := 0
	for _, queue := range queues {
		if *queueName != "" && queue.Name != *queueName {
			continue
		}
		_, _ = fmt.Fprintf(stdout, "%s ready=%d in_flight=%d durable=%t\n", queue.Name, queue.Ready, queue.InFlight, queue.Durable)
		printed++
	}
	if printed == 0 {
		if *queueName != "" {
			_, _ = fmt.Fprintf(stdout, "queue %s not found\n", *queueName)
			return nil
		}
		_, _ = fmt.Fprintln(stdout, "no queues found")
	}
	return nil
}

func poolFromName(name string) q.Pool {
	switch q.PoolStrategy(name) {
	case q.PoolSolo:
		return q.NewSoloPool()
	case q.PoolProcessBacked:
		return q.NewProcessPool(q.ProcessPoolOptions{})
	default:
		return q.NewGoroutinePool()
	}
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func parsePair(value string) (int, int, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected min,max")
	}
	first, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, err
	}
	second, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, err
	}
	return first, second, nil
}

func parseKeyValue(value string) (string, string, error) {
	key, val, ok := strings.Cut(value, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return "", "", fmt.Errorf("expected key=value")
	}
	return strings.TrimSpace(key), strings.TrimSpace(val), nil
}

func parseRateLimit(value string) (string, q.RateLimit, error) {
	task, raw, err := parseKeyValue(value)
	if err != nil {
		return "", q.RateLimit{}, err
	}
	limitText, periodText, ok := strings.Cut(raw, "/")
	if !ok {
		return "", q.RateLimit{}, fmt.Errorf("expected task=limit/period")
	}
	limit, err := strconv.Atoi(limitText)
	if err != nil {
		return "", q.RateLimit{}, err
	}
	period, err := time.ParseDuration(periodText)
	if err != nil {
		return "", q.RateLimit{}, err
	}
	return task, q.RateLimit{Limit: limit, Period: period}, nil
}

func parseTimeLimit(value string) (string, time.Duration, time.Duration, error) {
	task, raw, err := parseKeyValue(value)
	if err != nil {
		return "", 0, 0, err
	}
	softText, hardText, ok := strings.Cut(raw, ",")
	if !ok {
		return "", 0, 0, fmt.Errorf("expected task=soft,hard")
	}
	soft, err := time.ParseDuration(softText)
	if err != nil {
		return "", 0, 0, err
	}
	hard, err := time.ParseDuration(hardText)
	if err != nil {
		return "", 0, 0, err
	}
	return task, soft, hard, nil
}

func firstWorker(runtime *QueueRuntime) *q.Worker {
	if runtime == nil || len(runtime.Workers) == 0 {
		return nil
	}
	return runtime.Workers[0]
}
