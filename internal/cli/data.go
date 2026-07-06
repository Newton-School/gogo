package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Newton-School/gogo/orm"
)

var ErrInvalidFixture = errors.New("invalid fixture")

// FixtureRecord is one serialized model row.
type FixtureRecord struct {
	Model      string         `json:"model"`
	PK         any            `json:"pk,omitempty"`
	NaturalKey []any          `json:"natural_key,omitempty"`
	Fields     map[string]any `json:"fields,omitempty"`
}

// FixtureQuery configures dumpdata selection.
type FixtureQuery struct {
	Labels         []string
	Database       string
	NaturalForeign bool
	NaturalPrimary bool
}

// FixtureLoadOptions configures loaddata behavior.
type FixtureLoadOptions struct {
	Database    string
	Transaction bool
}

// FixtureStore is the persistence boundary for fixture commands.
type FixtureStore interface {
	Dump(context.Context, FixtureQuery) ([]FixtureRecord, error)
	Load(context.Context, []FixtureRecord, FixtureLoadOptions) error
}

// MemoryFixtureStore is a deterministic fixture store for tests and local project wiring.
type MemoryFixtureStore struct {
	mu      sync.RWMutex
	records []FixtureRecord
}

func NewMemoryFixtureStore(records ...FixtureRecord) *MemoryFixtureStore {
	return &MemoryFixtureStore{records: cloneFixtureRecords(records)}
}

type metadataFixtureStore struct {
	store *orm.MetadataStore
}

type errorFixtureStore struct {
	err error
}

// NewMetadataFixtureStore creates a database-backed fixture store.
func NewMetadataFixtureStore(store *orm.MetadataStore) FixtureStore {
	return metadataFixtureStore{store: store}
}

// NewErrorFixtureStore creates a fixture store that fails every operation.
func NewErrorFixtureStore(err error) FixtureStore {
	return errorFixtureStore{err: err}
}

func (s errorFixtureStore) Dump(context.Context, FixtureQuery) ([]FixtureRecord, error) {
	return nil, s.err
}

func (s errorFixtureStore) Load(context.Context, []FixtureRecord, FixtureLoadOptions) error {
	return s.err
}

func (s metadataFixtureStore) Dump(ctx context.Context, query FixtureQuery) ([]FixtureRecord, error) {
	if s.store == nil {
		return nil, orm.ErrDatabaseNotFound
	}
	records, err := s.store.Dump(ctx, query.Labels)
	if err != nil {
		return nil, err
	}
	fixtures := make([]FixtureRecord, len(records))
	for i, record := range records {
		fixtures[i] = FixtureRecord{Model: record.Model, PK: record.PK, Fields: cloneFixtureFields(record.Fields)}
	}
	return fixtures, nil
}

func (s metadataFixtureStore) Load(ctx context.Context, records []FixtureRecord, _ FixtureLoadOptions) error {
	if s.store == nil {
		return orm.ErrDatabaseNotFound
	}
	converted := make([]orm.MetadataFixtureRecord, len(records))
	for i, record := range records {
		converted[i] = orm.MetadataFixtureRecord{Model: record.Model, PK: record.PK, Fields: cloneFixtureFields(record.Fields)}
	}
	return s.store.Load(ctx, converted)
}

func (s *MemoryFixtureStore) Dump(_ context.Context, query FixtureQuery) ([]FixtureRecord, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(query.Labels) == 0 {
		return cloneFixtureRecords(s.records), nil
	}
	labels := map[string]struct{}{}
	for _, label := range query.Labels {
		labels[label] = struct{}{}
	}
	var records []FixtureRecord
	for _, record := range s.records {
		if _, ok := labels[record.Model]; ok {
			records = append(records, cloneFixtureRecord(record))
		}
	}
	return records, nil
}

func (s *MemoryFixtureStore) Load(_ context.Context, records []FixtureRecord, _ FixtureLoadOptions) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, cloneFixtureRecords(records)...)
	return nil
}

// FixtureSerializer encodes and decodes fixture records.
type FixtureSerializer interface {
	Format() string
	Dump([]FixtureRecord, int) ([]byte, error)
	Load([]byte) ([]FixtureRecord, error)
}

// FixtureSerializerRegistry stores serializers by format.
type FixtureSerializerRegistry struct {
	serializers map[string]FixtureSerializer
}

func NewFixtureSerializerRegistry() *FixtureSerializerRegistry {
	registry := &FixtureSerializerRegistry{serializers: map[string]FixtureSerializer{}}
	registry.Register(jsonFixtureSerializer{})
	registry.Register(jsonlFixtureSerializer{})
	registry.Register(xmlFixtureSerializer{})
	return registry
}

func (r *FixtureSerializerRegistry) Register(serializer FixtureSerializer) {
	if serializer == nil {
		return
	}
	r.serializers[strings.ToLower(serializer.Format())] = serializer
}

func (r *FixtureSerializerRegistry) Get(format string) (FixtureSerializer, bool) {
	serializer, ok := r.serializers[strings.ToLower(format)]
	return serializer, ok
}

// NewDumpdataCommand creates the dumpdata command.
func NewDumpdataCommand(store FixtureStore, serializers ...FixtureSerializer) Command {
	if store == nil {
		store = NewMemoryFixtureStore()
	}
	return dataCommand{name: "dumpdata", summary: "Dump fixture data", store: store, serializers: fixtureRegistry(serializers...)}
}

// NewLoaddataCommand creates the loaddata command.
func NewLoaddataCommand(store FixtureStore, serializers ...FixtureSerializer) Command {
	if store == nil {
		store = NewMemoryFixtureStore()
	}
	return dataCommand{name: "loaddata", summary: "Load fixture data", store: store, serializers: fixtureRegistry(serializers...)}
}

type dataCommand struct {
	name        string
	summary     string
	store       FixtureStore
	serializers *FixtureSerializerRegistry
}

func (c dataCommand) Name() string    { return c.name }
func (c dataCommand) Summary() string { return c.summary }

func (c dataCommand) Run(ctx context.Context, args []string) error {
	return c.runWithIO(ctx, args, io.Discard, io.Discard)
}

func (c dataCommand) runWithIO(ctx context.Context, args []string, stdout, _ io.Writer) error {
	switch c.name {
	case "dumpdata":
		return c.runDumpdata(ctx, args, stdout)
	case "loaddata":
		return c.runLoaddata(ctx, args, stdout)
	default:
		return fmt.Errorf("%w: unknown data command %s", ErrCommandFailed, c.name)
	}
}

func (c dataCommand) runDumpdata(ctx context.Context, args []string, stdout io.Writer) error {
	options, err := parseDumpdataFlags(args)
	if err != nil {
		return err
	}
	serializer, ok := c.serializers.Get(options.format)
	if !ok {
		return fmt.Errorf("%w: unsupported fixture format %s", ErrInvalidFixture, options.format)
	}
	records, err := c.store.Dump(ctx, options.query)
	if err != nil {
		return fmt.Errorf("%w: dump fixtures: %v", ErrCommandFailed, err)
	}
	records = prepareDumpRecords(records, options.query)
	encoded, err := serializer.Dump(records, options.indent)
	if err != nil {
		return err
	}
	if _, err := stdout.Write(encoded); err != nil {
		return fmt.Errorf("%w: write dumpdata output: %v", ErrCommandFailed, err)
	}
	if len(encoded) == 0 || encoded[len(encoded)-1] != '\n' {
		if _, err := fmt.Fprintln(stdout); err != nil {
			return fmt.Errorf("%w: write dumpdata newline: %v", ErrCommandFailed, err)
		}
	}
	return nil
}

func (c dataCommand) runLoaddata(ctx context.Context, args []string, stdout io.Writer) error {
	options, err := parseLoaddataFlags(args)
	if err != nil {
		return err
	}
	serializer, ok := c.serializers.Get(options.format)
	if !ok {
		return fmt.Errorf("%w: unsupported fixture format %s", ErrInvalidFixture, options.format)
	}
	var records []FixtureRecord
	for _, fixturePath := range options.paths {
		data, err := os.ReadFile(fixturePath)
		if err != nil {
			return fmt.Errorf("%w: read fixture %s: %v", ErrCommandFailed, fixturePath, err)
		}
		decoded, err := serializer.Load(data)
		if err != nil {
			return err
		}
		records = append(records, decoded...)
	}
	if err := validateFixtureDuplicates(records); err != nil {
		return err
	}
	if err := c.store.Load(ctx, records, options.load); err != nil {
		return fmt.Errorf("%w: load fixtures: %v", ErrCommandFailed, err)
	}
	if _, err := fmt.Fprintf(stdout, "loaded %d object(s) from %d fixture(s)\n", len(records), len(options.paths)); err != nil {
		return fmt.Errorf("%w: write loaddata summary: %v", ErrCommandFailed, err)
	}
	return nil
}

type dumpdataOptions struct {
	format string
	indent int
	query  FixtureQuery
}

func parseDumpdataFlags(args []string) (dumpdataOptions, error) {
	flags := flag.NewFlagSet("dumpdata", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := dumpdataOptions{format: "json", query: FixtureQuery{Database: "default"}}
	flags.StringVar(&options.format, "format", "json", "fixture format")
	flags.IntVar(&options.indent, "indent", 0, "JSON indentation")
	flags.StringVar(&options.query.Database, "database", "default", "database alias")
	flags.BoolVar(&options.query.NaturalForeign, "natural-foreign", false, "use natural foreign keys")
	flags.BoolVar(&options.query.NaturalPrimary, "natural-primary", false, "use natural primary keys")
	if err := flags.Parse(args); err != nil {
		return dumpdataOptions{}, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	options.query.Labels = append([]string(nil), flags.Args()...)
	return options, nil
}

type loaddataOptions struct {
	format string
	load   FixtureLoadOptions
	paths  []string
}

func parseLoaddataFlags(args []string) (loaddataOptions, error) {
	flags := flag.NewFlagSet("loaddata", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := loaddataOptions{format: "json", load: FixtureLoadOptions{Database: "default"}}
	flags.StringVar(&options.format, "format", "json", "fixture format")
	flags.StringVar(&options.load.Database, "database", "default", "database alias")
	flags.BoolVar(&options.load.Transaction, "transaction", false, "wrap loading in a transaction")
	if err := flags.Parse(args); err != nil {
		return loaddataOptions{}, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	options.paths = append([]string(nil), flags.Args()...)
	if len(options.paths) == 0 {
		return loaddataOptions{}, fmt.Errorf("%w: fixture path is required", ErrInvalidArguments)
	}
	if options.format == "" {
		options.format = strings.TrimPrefix(filepath.Ext(options.paths[0]), ".")
	}
	return options, nil
}

func fixtureRegistry(serializers ...FixtureSerializer) *FixtureSerializerRegistry {
	registry := NewFixtureSerializerRegistry()
	for _, serializer := range serializers {
		registry.Register(serializer)
	}
	return registry
}

func prepareDumpRecords(records []FixtureRecord, query FixtureQuery) []FixtureRecord {
	prepared := make([]FixtureRecord, len(records))
	for i, record := range records {
		record.Fields = cloneFixtureFields(record.Fields)
		if len(record.NaturalKey) == 0 {
			record.NaturalKey = inferNaturalKey(record)
		}
		if query.NaturalPrimary && len(record.NaturalKey) > 0 {
			record.PK = nil
		}
		prepared[i] = record
	}
	return prepared
}

func inferNaturalKey(record FixtureRecord) []any {
	switch record.Model {
	case "contenttypes.ContentType":
		if record.Fields["app_label"] != nil && record.Fields["model"] != nil {
			return []any{record.Fields["app_label"], record.Fields["model"]}
		}
	case "auth.Permission":
		if record.Fields["content_type"] != nil && record.Fields["codename"] != nil {
			return []any{record.Fields["content_type"], record.Fields["codename"]}
		}
	}
	return nil
}

func validateFixtureDuplicates(records []FixtureRecord) error {
	seen := map[string]struct{}{}
	for _, record := range records {
		key := fixtureIdentity(record)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate %s", ErrInvalidFixture, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func fixtureIdentity(record FixtureRecord) string {
	if record.Model == "" {
		return ""
	}
	if record.PK != nil {
		return record.Model + "|pk|" + fmt.Sprint(record.PK)
	}
	if len(record.NaturalKey) > 0 {
		return record.Model + "|natural|" + fmt.Sprint(record.NaturalKey)
	}
	return ""
}

func cloneFixtureFields(fields map[string]any) map[string]any {
	cloned := make(map[string]any, len(fields))
	for key, value := range fields {
		cloned[key] = value
	}
	return cloned
}

func cloneFixtureRecords(records []FixtureRecord) []FixtureRecord {
	cloned := make([]FixtureRecord, len(records))
	for index, record := range records {
		cloned[index] = cloneFixtureRecord(record)
	}
	return cloned
}

func cloneFixtureRecord(record FixtureRecord) FixtureRecord {
	record.NaturalKey = append([]any(nil), record.NaturalKey...)
	record.Fields = cloneFixtureFields(record.Fields)
	return record
}

type jsonFixtureSerializer struct{}

func (jsonFixtureSerializer) Format() string { return "json" }

func (jsonFixtureSerializer) Dump(records []FixtureRecord, indent int) ([]byte, error) {
	if indent > 0 {
		return json.MarshalIndent(records, "", strings.Repeat(" ", indent))
	}
	return json.Marshal(records)
}

func (jsonFixtureSerializer) Load(data []byte) ([]FixtureRecord, error) {
	var records []FixtureRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON fixture: %v", ErrInvalidFixture, err)
	}
	return records, nil
}

type jsonlFixtureSerializer struct{}

func (jsonlFixtureSerializer) Format() string { return "jsonl" }

func (jsonlFixtureSerializer) Dump(records []FixtureRecord, _ int) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			return nil, err
		}
	}
	return buffer.Bytes(), nil
}

func (jsonlFixtureSerializer) Load(data []byte) ([]FixtureRecord, error) {
	var records []FixtureRecord
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record FixtureRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("%w: invalid JSONL fixture: %v", ErrInvalidFixture, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

type xmlFixtureSerializer struct{}

func (xmlFixtureSerializer) Format() string { return "xml" }

func (xmlFixtureSerializer) Dump(records []FixtureRecord, indent int) ([]byte, error) {
	fixtures := xmlFixtures{Objects: make([]xmlObject, len(records))}
	for i, record := range records {
		fixtures.Objects[i] = toXMLObject(record)
	}
	if indent > 0 {
		return xml.MarshalIndent(fixtures, "", strings.Repeat(" ", indent))
	}
	return xml.Marshal(fixtures)
}

func (xmlFixtureSerializer) Load(data []byte) ([]FixtureRecord, error) {
	var fixtures xmlFixtures
	if err := xml.Unmarshal(data, &fixtures); err != nil {
		return nil, fmt.Errorf("%w: invalid XML fixture: %v", ErrInvalidFixture, err)
	}
	records := make([]FixtureRecord, len(fixtures.Objects))
	for i, object := range fixtures.Objects {
		records[i] = fromXMLObject(object)
	}
	return records, nil
}

type xmlFixtures struct {
	XMLName xml.Name    `xml:"fixtures"`
	Objects []xmlObject `xml:"object"`
}

type xmlObject struct {
	Model      string     `xml:"model,attr"`
	PK         string     `xml:"pk,attr,omitempty"`
	NaturalKey string     `xml:"natural_key,attr,omitempty"`
	Fields     []xmlField `xml:"field"`
}

type xmlField struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

func toXMLObject(record FixtureRecord) xmlObject {
	object := xmlObject{Model: record.Model, PK: fmt.Sprint(record.PK)}
	if record.PK == nil {
		object.PK = ""
	}
	if len(record.NaturalKey) > 0 {
		values := make([]string, len(record.NaturalKey))
		for i, value := range record.NaturalKey {
			values[i] = fmt.Sprint(value)
		}
		object.NaturalKey = strings.Join(values, "|")
	}
	keys := make([]string, 0, len(record.Fields))
	for key := range record.Fields {
		keys = append(keys, key)
	}
	sortStrings(keys)
	for _, key := range keys {
		object.Fields = append(object.Fields, xmlField{Name: key, Value: fmt.Sprint(record.Fields[key])})
	}
	return object
}

func fromXMLObject(object xmlObject) FixtureRecord {
	record := FixtureRecord{Model: object.Model, Fields: map[string]any{}}
	if object.PK != "" {
		record.PK = object.PK
	}
	if object.NaturalKey != "" {
		for _, value := range strings.Split(object.NaturalKey, "|") {
			record.NaturalKey = append(record.NaturalKey, value)
		}
	}
	for _, field := range object.Fields {
		record.Fields[field.Name] = field.Value
	}
	return record
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
