package static

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxManifestBytes = 16 << 20

// Asset is public deployment metadata, never a source filesystem location.
type Asset struct {
	Path      string `json:"path"`
	Versioned string `json:"versioned"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}
type manifestDocument struct {
	Version int     `json:"version"`
	Assets  []Asset `json:"assets"`
}

// Manifest is an immutable lookup snapshot. Reload explicitly after a new
// deployment; template rendering never reads a file or follows a network URL.
type Manifest struct {
	prefix  urlPrefix
	assets  map[string]Asset
	encoded []byte
}

func (m *Manifest) Assets() []Asset {
	if m == nil {
		return nil
	}
	result := make([]Asset, 0, len(m.assets))
	for _, a := range m.assets {
		result = append(result, a)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}
func (m *Manifest) JSON() []byte {
	if m == nil {
		return nil
	}
	return append([]byte(nil), m.encoded...)
}
func (m *Manifest) URL(name string) (string, error) {
	if m == nil || m.assets == nil || !validPath(name) || ignoredPath(name) {
		return "", ErrInvalid
	}
	asset, ok := m.assets[name]
	if !ok {
		return "", ErrNotFound
	}
	return assetURL(m.prefix, asset.Versioned), nil
}
func (m *Manifest) Prefix() string {
	if m == nil {
		return ""
	}
	return m.prefix.base
}

// ReadManifest accepts the bounded versioned JSON format. The supplied reader
// remains caller-owned. Unknown versions, duplicate names, invalid hashes and
// noncanonical hashed paths fail closed, with no unhashed fallback.
func ReadManifest(ctx context.Context, reader io.Reader, baseURL string) (result *Manifest, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = fail(ErrInvalid, contextError(ctx))
		}
	}()
	if nilValue(reader) {
		return nil, ErrInvalid
	}
	prefix, e := parsePrefix(baseURL)
	if e != nil {
		return nil, e
	}
	if e = contextError(ctx); e != nil {
		return nil, e
	}
	data, e := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: reader}, maxManifestBytes+1))
	if e != nil {
		return nil, fail(ErrSource, e)
	}
	if len(data) > maxManifestBytes {
		return nil, ErrLimit
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	// Decode the outer schema and asset array incrementally so a compact input
	// cannot allocate millions of empty descriptors before the count check.
	var document manifestDocument
	start, e := decoder.Token()
	if e != nil || start != json.Delim('{') {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, e := decoder.Token()
		if e != nil {
			return nil, ErrInvalid
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return nil, ErrInvalid
		}
		seen[name] = true
		switch name {
		case "version":
			if e = decoder.Decode(&document.Version); e != nil {
				return nil, ErrInvalid
			}
		case "assets":
			token, e := decoder.Token()
			if e != nil || token != json.Delim('[') {
				return nil, ErrInvalid
			}
			for decoder.More() {
				if len(document.Assets) >= 100000 {
					return nil, ErrLimit
				}
				var asset Asset
				if e = decoder.Decode(&asset); e != nil {
					return nil, ErrInvalid
				}
				document.Assets = append(document.Assets, asset)
			}
			if token, e = decoder.Token(); e != nil || token != json.Delim(']') {
				return nil, ErrInvalid
			}
		default:
			return nil, ErrInvalid
		}
	}
	if token, e := decoder.Token(); e != nil || token != json.Delim('}') || !seen["version"] || !seen["assets"] {
		return nil, ErrInvalid
	}
	var trailing any
	if e = decoder.Decode(&trailing); !errors.Is(e, io.EOF) {
		return nil, ErrInvalid
	}
	if document.Version != 1 || len(document.Assets) > 100000 {
		return nil, ErrInvalid
	}
	m := &Manifest{prefix: prefix, assets: map[string]Asset{}}
	for _, asset := range document.Assets {
		if !validPath(asset.Path) || ignoredPath(asset.Path) || !validPath(asset.Versioned) || asset.Size < 0 || asset.Size > 256<<20 || !validDigest(asset.SHA256) || asset.Versioned != hashedName(asset.Path, asset.SHA256) {
			return nil, ErrInvalid
		}
		if _, ok := m.assets[asset.Path]; ok {
			return nil, ErrInvalid
		}
		m.assets[asset.Path] = asset
	}
	if e = m.encode(); e != nil {
		return nil, e
	}
	if e = contextError(ctx); e != nil {
		return nil, e
	}
	return m, nil
}
func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func hashedName(name, digest string) string { return path.Join(path.Dir(name), digest+path.Ext(name)) }
func digest(data []byte) string             { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (m *Manifest) encode() error {
	size := len(`{"version":1,"assets":[]}`) + 1
	for _, asset := range m.assets {
		size += len(`{"path":,"versioned":,"sha256":,"size":}`) + jsonStringSize(asset.Path) + jsonStringSize(asset.Versioned) + jsonStringSize(asset.SHA256) + len(strconv.FormatInt(asset.Size, 10)) + 1
		if size > maxManifestBytes {
			return ErrLimit
		}
	}
	data, err := json.Marshal(manifestDocument{Version: 1, Assets: m.Assets()})
	if err != nil {
		return ErrInvalid
	}
	if len(data) > maxManifestBytes {
		return ErrLimit
	}
	m.encoded = append(data, '\n')
	return nil
}
func jsonStringSize(s string) int {
	size := 2 + len(s)
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		s = s[n:]
		switch r {
		case '<', '>', '&':
			size += 5
		case '\u2028', '\u2029':
			size += 3
		case '\\', '"':
			size++
		default:
			if r < 32 {
				size += 5
			}
		}
	}
	return size
}

type preparedSet struct {
	manifest *Manifest
	bytes    map[string][]byte
}

func (c Collector) prepare(ctx context.Context, input map[string][]byte) (preparedSet, error) {
	m := &Manifest{prefix: c.prefix, assets: map[string]Asset{}}
	output := map[string][]byte{}
	state := map[string]uint8{}
	var total int64
	references := 0
	var visit func(string, int) (Asset, error)
	visit = func(name string, depth int) (Asset, error) {
		if depth > 64 {
			return Asset{}, ErrLimit
		}
		if err := contextError(ctx); err != nil {
			return Asset{}, err
		}
		if state[name] == 1 {
			return Asset{}, ErrDependency
		}
		if state[name] == 2 {
			return m.assets[name], nil
		}
		data, ok := input[name]
		if !ok {
			return Asset{}, ErrDependency
		}
		if len(path.Ext(name)) > 32 {
			return Asset{}, ErrInvalid
		}
		state[name] = 1
		if strings.EqualFold(path.Ext(name), ".css") {
			if int64(len(data)) > c.config.Limits.MaxCSSBytes {
				return Asset{}, ErrLimit
			}
			var err error
			data, err = rewriteCSS(ctx, data, c.config.Limits.MaxCSSBytes, func(raw string) (string, error) {
				references++
				if references > c.config.Limits.MaxReferences {
					return "", ErrLimit
				}
				dependency, suffix, local, err := c.cssReference(name, raw)
				if err != nil {
					return "", err
				}
				if !local {
					return raw, nil
				}
				asset, err := visit(dependency, depth+1)
				if err != nil {
					return "", err
				}
				return relativeAsset(path.Dir(name), asset.Versioned) + suffix, nil
			})
			if err != nil {
				return Asset{}, err
			}
		}
		if int64(len(data)) > c.config.Limits.MaxFileBytes || int64(len(data)) > c.config.Limits.MaxTotalBytes-total {
			return Asset{}, ErrLimit
		}
		total += int64(len(data))
		asset := Asset{Path: name, SHA256: digest(data), Size: int64(len(data))}
		asset.Versioned = hashedName(name, asset.SHA256)
		if !validPath(asset.Versioned) {
			return Asset{}, ErrInvalid
		}
		m.assets[name] = asset
		output[asset.Versioned] = data
		state[name] = 2
		return asset, nil
	}
	names := make([]string, 0, len(input))
	for name := range input {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := visit(name, 0); err != nil {
			return preparedSet{}, err
		}
	}
	if err := m.encode(); err != nil {
		return preparedSet{}, err
	}
	return preparedSet{m, output}, nil
}

// Collect reads and validates a complete bounded set before any destination
// write. Dry-run uses the same hashing and dependency checks but performs no
// destination opens, directory creation or file writes.
func (c *Collector) Collect(ctx context.Context, options CollectOptions) (report Report, err error) {
	defer func() {
		if recover() != nil {
			err = fail(ErrPublish, contextError(ctx))
			if !report.Published {
				report = Report{}
			}
		}
		if err != nil {
			// Include failures returned before the local publisher acquires its
			// root, and every deferred descriptor/cleanup failure, in one safe
			// public boundary. Publication and error identity remain intact.
			err = fail(ErrPublish, err)
		}
	}()
	owned, e := c.operation(ctx)
	if e != nil {
		return Report{}, e
	}
	if owned.config.Destination == "" {
		return Report{}, ErrInvalid
	}
	if e = owned.checkRootSeparation(); e != nil {
		return Report{}, e
	}
	input, e := owned.discover(ctx, true)
	if e != nil {
		return Report{}, e
	}
	set, e := owned.prepare(ctx, input.assets)
	if e != nil {
		return Report{}, e
	}
	report = Report{DryRun: options.DryRun, Manifest: set.manifest, Matches: append([]Match(nil), input.matches...)}
	if e = contextError(ctx); e != nil {
		return Report{}, e
	}
	if options.DryRun {
		return report, nil
	}
	report.Published, err = owned.publish(ctx, set)
	if err != nil && !report.Published {
		return Report{}, err
	}
	return report, err
}
