// Package redis provides a real Redis-backed beat schedule store.
package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Newton-School/gogo/queue"
	"github.com/google/uuid"
	redisclient "github.com/redis/go-redis/v9"
)

type Config struct {
	URL    string
	Prefix string
	Now    func() time.Time
	Client *redisclient.Client
}

type Store struct {
	config Config
	client *redisclient.Client
	closed atomic.Bool
}

type Keys struct {
	Entries    string
	LockPrefix string
}

func init() {
	queue.RegisterScheduleStoreFactory("redis", func(config queue.RuntimeConfig) (queue.ScheduleStore, error) {
		return NewStoreFromURL(config.ScheduleStore)
	})
	queue.RegisterScheduleStoreFactory("rediss", func(config queue.RuntimeConfig) (queue.ScheduleStore, error) {
		return NewStoreFromURL(config.ScheduleStore)
	})
}

func NewStore(config Config) *Store {
	store, _ := newStore(config, false)
	return store
}

func NewStoreFromURL(rawURL string) (*Store, error) {
	return newStore(Config{URL: rawURL}, true)
}

func newStore(config Config, strictURL bool) (*Store, error) {
	if config.Prefix == "" {
		config.Prefix = "gogo"
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	client := config.Client
	if client == nil && strings.TrimSpace(config.URL) != "" {
		options, err := redisclient.ParseURL(config.URL)
		if err != nil {
			if strictURL {
				return nil, fmt.Errorf("%w: parse Redis schedule store URL %q: %v", queue.ErrUnsupportedRuntimeURL, config.URL, err)
			}
		} else {
			client = redisclient.NewClient(options)
		}
	}
	return &Store{config: config, client: client}, nil
}

func (s *Store) Keys() Keys {
	prefix := s.config.Prefix
	if prefix == "" {
		prefix = "gogo"
	}
	return Keys{
		Entries:    prefix + ":schedule:entries",
		LockPrefix: prefix + ":schedule:locks:",
	}
}

func (k Keys) Lock(name string) string {
	return k.LockPrefix + name
}

func (s *Store) List(ctx context.Context) ([]queue.ScheduleEntry, error) {
	if err := s.ensureClient(); err != nil {
		return nil, err
	}
	values, err := s.client.HGetAll(ctx, s.Keys().Entries).Result()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]queue.ScheduleEntry, 0, len(names))
	for _, name := range names {
		entry, err := decodeEntry([]byte(values[name]))
		if err != nil {
			return nil, fmt.Errorf("decode schedule entry %q: %w", name, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *Store) Save(ctx context.Context, entry queue.ScheduleEntry) error {
	if err := s.ensureClient(); err != nil {
		return err
	}
	record, err := encodeEntry(entry)
	if err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.client.HSet(ctx, s.Keys().Entries, entry.Name, data).Err()
}

func (s *Store) Lock(ctx context.Context, name string, ttl time.Duration) (queue.ScheduleLock, error) {
	if err := s.ensureClient(); err != nil {
		return nil, err
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	token := uuid.NewString()
	key := s.Keys().Lock(name)
	ok, err := s.client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, queue.ErrScheduleLocked
	}
	return redisScheduleLock{store: s, key: key, token: token}, nil
}

func (s *Store) Ping(ctx context.Context) error {
	if err := s.ensureClient(); err != nil {
		return err
	}
	return s.client.Ping(ctx).Err()
}

func (s *Store) Close() error {
	if s == nil || s.client == nil || s.closed.Swap(true) {
		return nil
	}
	return s.client.Close()
}

func (s *Store) ensureClient() error {
	if s == nil || s.client == nil {
		return fmt.Errorf("%w: Redis schedule store is not configured", queue.ErrUnsupportedRuntimeURL)
	}
	if s.closed.Load() {
		return fmt.Errorf("%w: Redis schedule store is closed", queue.ErrUnsupportedRuntimeURL)
	}
	return nil
}

type redisScheduleLock struct {
	store *Store
	key   string
	token string
}

func (l redisScheduleLock) Release(ctx context.Context) error {
	if err := l.store.ensureClient(); err != nil {
		return err
	}
	const script = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
	return l.store.client.Eval(ctx, script, []string{l.key}, l.token).Err()
}

type entryRecord struct {
	Name          string            `json:"name"`
	Signature     queue.Signature   `json:"signature"`
	Schedule      scheduleRecord    `json:"schedule"`
	Enabled       bool              `json:"enabled"`
	OneOff        bool              `json:"one_off"`
	LastRunAt     *time.Time        `json:"last_run_at,omitempty"`
	TotalRunCount int               `json:"total_run_count"`
	Send          sendOptionsRecord `json:"send"`
}

type sendOptionsRecord struct {
	ID            string    `json:"id,omitempty"`
	RootID        string    `json:"root_id,omitempty"`
	ParentID      string    `json:"parent_id,omitempty"`
	GroupID       string    `json:"group_id,omitempty"`
	ChordID       string    `json:"chord_id,omitempty"`
	Retries       int       `json:"retries,omitempty"`
	ReplyTo       string    `json:"reply_to,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
}

type scheduleRecord struct {
	Type       string    `json:"type"`
	EveryNanos int64     `json:"every_nanos,omitempty"`
	StartAt    time.Time `json:"start_at,omitempty"`
	Minute     string    `json:"minute,omitempty"`
	Hour       string    `json:"hour,omitempty"`
	DayOfMonth string    `json:"day_of_month,omitempty"`
	Month      string    `json:"month,omitempty"`
	DayOfWeek  string    `json:"day_of_week,omitempty"`
	Location   string    `json:"location,omitempty"`
	RunAt      time.Time `json:"run_at,omitempty"`
	Event      string    `json:"event,omitempty"`
	Latitude   float64   `json:"latitude,omitempty"`
	Longitude  float64   `json:"longitude,omitempty"`
}

func encodeEntry(entry queue.ScheduleEntry) (entryRecord, error) {
	if strings.TrimSpace(entry.Name) == "" {
		return entryRecord{}, fmt.Errorf("schedule entry name is required")
	}
	schedule, err := encodeSchedule(entry.Schedule)
	if err != nil {
		return entryRecord{}, err
	}
	return entryRecord{
		Name:          entry.Name,
		Signature:     entry.Signature.Clone(),
		Schedule:      schedule,
		Enabled:       entry.Enabled,
		OneOff:        entry.OneOff,
		LastRunAt:     cloneTime(entry.LastRunAt),
		TotalRunCount: entry.TotalRunCount,
		Send:          encodeSendOptions(entry.Send),
	}, nil
}

func decodeEntry(data []byte) (queue.ScheduleEntry, error) {
	var record entryRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return queue.ScheduleEntry{}, err
	}
	schedule, err := decodeSchedule(record.Schedule)
	if err != nil {
		return queue.ScheduleEntry{}, err
	}
	return queue.ScheduleEntry{
		Name:          record.Name,
		Signature:     record.Signature.Clone(),
		Schedule:      schedule,
		Enabled:       record.Enabled,
		OneOff:        record.OneOff,
		LastRunAt:     cloneTime(record.LastRunAt),
		TotalRunCount: record.TotalRunCount,
		Send:          decodeSendOptions(record.Send),
	}, nil
}

func encodeSendOptions(options queue.SendOptions) sendOptionsRecord {
	return sendOptionsRecord{
		ID:            options.ID,
		RootID:        options.RootID,
		ParentID:      options.ParentID,
		GroupID:       options.GroupID,
		ChordID:       options.ChordID,
		Retries:       options.Retries,
		ReplyTo:       options.ReplyTo,
		CorrelationID: options.CorrelationID,
		CreatedAt:     options.CreatedAt,
	}
}

func decodeSendOptions(record sendOptionsRecord) queue.SendOptions {
	return queue.SendOptions{
		ID:            record.ID,
		RootID:        record.RootID,
		ParentID:      record.ParentID,
		GroupID:       record.GroupID,
		ChordID:       record.ChordID,
		Retries:       record.Retries,
		ReplyTo:       record.ReplyTo,
		CorrelationID: record.CorrelationID,
		CreatedAt:     record.CreatedAt,
	}
}

func encodeSchedule(schedule queue.Schedule) (scheduleRecord, error) {
	switch value := schedule.(type) {
	case queue.IntervalSchedule:
		return scheduleRecord{Type: "interval", EveryNanos: int64(value.Every), StartAt: value.StartAt}, nil
	case queue.CrontabSchedule:
		return scheduleRecord{
			Type:       "crontab",
			Minute:     value.Minute,
			Hour:       value.Hour,
			DayOfMonth: value.DayOfMonth,
			Month:      value.Month,
			DayOfWeek:  value.DayOfWeek,
			Location:   locationName(value.Location),
		}, nil
	case queue.ClockedSchedule:
		return scheduleRecord{Type: "clocked", RunAt: value.RunAt}, nil
	case queue.SolarSchedule:
		return scheduleRecord{
			Type:      "solar",
			Event:     string(value.Event),
			Latitude:  value.Latitude,
			Longitude: value.Longitude,
			Location:  locationName(value.Location),
		}, nil
	default:
		return scheduleRecord{}, fmt.Errorf("unsupported schedule type %T", schedule)
	}
}

func decodeSchedule(record scheduleRecord) (queue.Schedule, error) {
	switch record.Type {
	case "interval":
		return queue.IntervalSchedule{Every: time.Duration(record.EveryNanos), StartAt: record.StartAt}, nil
	case "crontab":
		location, err := loadLocation(record.Location)
		if err != nil {
			return nil, err
		}
		return queue.CrontabSchedule{
			Minute:     record.Minute,
			Hour:       record.Hour,
			DayOfMonth: record.DayOfMonth,
			Month:      record.Month,
			DayOfWeek:  record.DayOfWeek,
			Location:   location,
		}, nil
	case "clocked":
		return queue.ClockedSchedule{RunAt: record.RunAt}, nil
	case "solar":
		location, err := loadLocation(record.Location)
		if err != nil {
			return nil, err
		}
		return queue.SolarSchedule{
			Event:     queue.SolarEvent(record.Event),
			Latitude:  record.Latitude,
			Longitude: record.Longitude,
			Location:  location,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported schedule record type %q", record.Type)
	}
}

func locationName(location *time.Location) string {
	if location == nil {
		return ""
	}
	return location.String()
}

func loadLocation(name string) (*time.Location, error) {
	if name == "" {
		return nil, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load schedule location %q: %w", name, err)
	}
	return location, nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
