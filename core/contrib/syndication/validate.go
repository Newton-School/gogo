package syndication

import (
	"context"
	"mime"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/language"
)

func xmlText(value string, limit int) bool {
	if len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if c < 32 && c != '\t' && c != '\n' && c != '\r' || c == 0xfffe || c == 0xffff {
			return false
		}
	}
	return true
}

func parseOrigin(raw string) (*url.URL, error) {
	if !xmlText(raw, 2048) || strings.ContainsAny(raw, "\\\t\r\n ") {
		return nil, ErrInvalidFeed
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return nil, ErrInvalidFeed
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || strings.ContainsAny(host, "%\\\t\r\n /@") {
		return nil, ErrInvalidFeed
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return nil, ErrInvalidFeed
		}
		host = ip.String()
	} else {
		if len(host) > 253 {
			return nil, ErrInvalidFeed
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, ErrInvalidFeed
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
					return nil, ErrInvalidFeed
				}
			}
		}
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return nil, ErrInvalidFeed
		}
		if u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443" {
			port = ""
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, ErrInvalidFeed
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return &url.URL{Scheme: u.Scheme, Host: host}, nil
}

func (r *Renderer) link(raw string, own, enclosure bool) (string, error) {
	if !xmlText(raw, 2048) || raw == "" || strings.ContainsAny(raw, "\\\t\r\n ") {
		return "", ErrInvalidFeed
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || !urlComponent(u.Path) || !urlComponent(u.Fragment) || strings.HasPrefix(u.Path, "//") {
		return "", ErrInvalidFeed
	}
	query, err := url.QueryUnescape(u.RawQuery)
	if err != nil || !urlComponent(query) {
		return "", ErrInvalidFeed
	}
	if !u.IsAbs() {
		if u.Host != "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
			return "", ErrInvalidFeed
		}
		u = r.origin.ResolveReference(u)
	}
	if enclosure && (u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "") {
		return "", ErrNotPublic
	}
	origin, err := parseOrigin(u.Scheme + "://" + u.Host)
	if err != nil || own && origin.String() != r.origin.String() || !own && !r.allowed[origin.String()] {
		return "", ErrInvalidFeed
	}
	u.Scheme, u.Host = origin.Scheme, origin.Host
	return u.String(), nil
}

func urlComponent(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if c < 32 || c == 127 || c == '\\' {
			return false
		}
	}
	return true
}

func cloneTime(at time.Time) time.Time {
	at = at.UTC()
	zone := *at.Location()
	return at.In(&zone)
}
func validTime(at time.Time, required bool) bool {
	if at.IsZero() {
		return !required
	}
	_, offset := at.Zone()
	return at.Year() >= 1 && at.Year() <= 9999 && at.UTC().Year() >= 1 && at.UTC().Year() <= 9999 && offset > -86400 && offset < 86400
}
func cloneAuthor(author *Author) *Author {
	if author == nil {
		return nil
	}
	copy := *author
	return &copy
}
func cloneItem(item Item) Item {
	item.Author = cloneAuthor(item.Author)
	item.Categories = append([]Category(nil), item.Categories...)
	item.Enclosures = append([]Enclosure(nil), item.Enclosures...)
	item.Published, item.Updated = cloneTime(item.Published), cloneTime(item.Updated)
	return item
}
func cloneFeed(feed Feed) Feed {
	feed.Author = cloneAuthor(feed.Author)
	feed.Categories = append([]Category(nil), feed.Categories...)
	feed.Updated = cloneTime(feed.Updated)
	items := make([]Item, len(feed.Items))
	for i, item := range feed.Items {
		items[i] = cloneItem(item)
	}
	feed.Items = items
	return feed
}

func (r *Renderer) normalize(ctx context.Context, feed Feed) (Feed, error) {
	if len(feed.Items) > r.maxItems {
		return Feed{}, ErrLimit
	}
	budget := 0
	text := func(limit int, values ...string) error {
		for _, value := range values {
			if !xmlText(value, limit) {
				return ErrInvalidFeed
			}
			if len(value) > r.maxBytes-budget {
				return ErrLimit
			}
			budget += len(value)
		}
		return nil
	}
	category := func(values []Category) error {
		if len(values) > 64 {
			return ErrLimit
		}
		for _, value := range values {
			if value.Term == "" {
				return ErrInvalidFeed
			}
			if err := text(4096, value.Term, value.Label, value.Scheme); err != nil {
				return err
			}
		}
		return nil
	}
	author := func(value *Author) error {
		if value == nil {
			return nil
		}
		if value.Name == "" {
			return ErrInvalidFeed
		}
		if err := text(4096, value.Name, value.Email, value.URL); err != nil {
			return err
		}
		if value.Email != "" {
			parsed, err := mail.ParseAddress(value.Email)
			if err != nil || parsed.Address != value.Email || parsed.Name != "" {
				return ErrInvalidFeed
			}
		}
		return nil
	}
	if feed.Title == "" || !validTime(feed.Updated, true) {
		return Feed{}, ErrInvalidFeed
	}
	if err := text(4096, feed.ID, feed.Title, feed.Link, feed.FeedURL, feed.Language, feed.Copyright, feed.Generator); err != nil {
		return Feed{}, err
	}
	if err := text(64<<10, feed.Description.Value); err != nil {
		return Feed{}, err
	}
	if err := author(feed.Author); err != nil {
		return Feed{}, err
	}
	if err := category(feed.Categories); err != nil {
		return Feed{}, err
	}
	if feed.Language != "" {
		if _, err := language.Parse(feed.Language); err != nil {
			return Feed{}, ErrInvalidFeed
		}
	}
	seen := map[string]bool{}
	for _, item := range feed.Items {
		if err := ctx.Err(); err != nil {
			return Feed{}, err
		}
		if item.Title == "" && item.Description.Value == "" || !validTime(item.Updated, true) || !validTime(item.Published, false) || item.Updated.After(feed.Updated) || !item.Published.IsZero() && item.Published.After(item.Updated) {
			return Feed{}, ErrInvalidFeed
		}
		if err := text(4096, item.ID, item.Title, item.Link, item.Comments, item.Copyright); err != nil {
			return Feed{}, err
		}
		if err := text(64<<10, item.Description.Value); err != nil {
			return Feed{}, err
		}
		if err := author(item.Author); err != nil {
			return Feed{}, err
		}
		if err := category(item.Categories); err != nil {
			return Feed{}, err
		}
		if len(item.Enclosures) > 16 {
			return Feed{}, ErrLimit
		}
		for _, e := range item.Enclosures {
			if e.Length < 0 {
				return Feed{}, ErrInvalidFeed
			}
			if err := text(2048, e.URL, e.MIMEType); err != nil {
				return Feed{}, err
			}
		}
	}
	snapshot := cloneFeed(feed)
	var err error
	if snapshot.Link, err = r.link(snapshot.Link, true, false); err != nil {
		return Feed{}, err
	}
	if snapshot.FeedURL, err = r.link(snapshot.FeedURL, true, false); err != nil {
		return Feed{}, err
	}
	if snapshot.ID == "" {
		snapshot.ID = snapshot.FeedURL
	}
	if snapshot.Author != nil && snapshot.Author.URL != "" {
		if snapshot.Author.URL, err = r.link(snapshot.Author.URL, false, false); err != nil {
			return Feed{}, err
		}
	}
	for i := range snapshot.Items {
		item := &snapshot.Items[i]
		if item.Link, err = r.link(item.Link, false, false); err != nil {
			return Feed{}, err
		}
		if item.ID == "" {
			item.ID = item.Link
			item.IDIsPermalink = true
		}
		if item.IDIsPermalink {
			if item.ID, err = r.link(item.ID, false, false); err != nil {
				return Feed{}, err
			}
		}
		if seen[item.ID] {
			return Feed{}, ErrInvalidFeed
		}
		seen[item.ID] = true
		if item.Comments != "" {
			if item.Comments, err = r.link(item.Comments, false, false); err != nil {
				return Feed{}, err
			}
		}
		if item.Author != nil && item.Author.URL != "" {
			if item.Author.URL, err = r.link(item.Author.URL, false, false); err != nil {
				return Feed{}, err
			}
		}
		for j := range item.Enclosures {
			e := &item.Enclosures[j]
			if e.URL, err = r.link(e.URL, false, true); err != nil {
				return Feed{}, err
			}
			media, params, err := mime.ParseMediaType(e.MIMEType)
			if err != nil || len(params) != 0 || media == "" {
				return Feed{}, ErrInvalidFeed
			}
			e.MIMEType = media
		}
	}
	return snapshot, nil
}
