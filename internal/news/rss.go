package news

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

const (
	// RSSUserAgent identifies the bot to feed servers.
	RSSUserAgent = "KrakenBot/0.1 (RSS reader; personal use)"
	// DefaultMaxItems is how many feed items are considered per fetch.
	DefaultMaxItems = 20
	// MaxTitleRunes caps a cleaned item title.
	MaxTitleRunes = 200
	// MaxDescriptionRunes caps a cleaned item description.
	MaxDescriptionRunes = 300

	rssTimeout      = 15 * time.Second
	maxFeedBodySize = 10 << 20 // 10 MiB guard against runaway responses
)

// RSSAdapter fetches one RSS or Atom feed. Build it with NewRSSAdapter or
// RSSAdapterFromSpec; it must not be copied after first use.
type RSSAdapter struct {
	Name     string
	URL      string
	Client   *http.Client     // nil: default client with a 15 s timeout
	MaxItems int              // <= 0: DefaultMaxItems
	Now      func() time.Time // nil: domain.NowUTC

	mu        sync.Mutex
	firstSeen map[string]time.Time // undated item ID → when this adapter first saw it
}

// NewRSSAdapter returns an adapter for the feed at url labelled name.
func NewRSSAdapter(name, url string) *RSSAdapter {
	return &RSSAdapter{Name: name, URL: url}
}

// SourceName returns the feed's configured name.
func (a *RSSAdapter) SourceName() string {
	return a.Name
}

// FetchNews downloads and parses the feed. Items without a usable pubDate get
// the time this adapter first saw them, so they do not jump to the top of the
// newest-first order on every fetch.
func (a *RSSAdapter) FetchNews(ctx context.Context) ([]domain.NewsItem, error) {
	body, err := a.download(ctx)
	if err != nil {
		log.Warn(a.Name+" RSS fetch failed", "url", a.URL, "error", err.Error())
		return []domain.NewsItem{}, err
	}
	maxItems := a.MaxItems
	if maxItems <= 0 {
		maxItems = DefaultMaxItems
	}
	parsed, err := parseRSSItems(body, a.Name, maxItems)
	if err != nil {
		log.Warn(a.Name+" RSS parse failed", "error", err.Error())
		return []domain.NewsItem{}, err
	}
	items := a.applyFirstSeen(parsed)
	log.Info(fmt.Sprintf("Fetched %d items from %s", len(items), a.Name))
	return items, nil
}

// applyFirstSeen dates undated items with the time they were first seen and
// forgets items that have left the feed, so the map stays bounded.
func (a *RSSAdapter) applyFirstSeen(parsed []parsedItem) []domain.NewsItem {
	now := domain.NowUTC()
	if a.Now != nil {
		now = NormalisePublishedAt(a.Now())
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	seen := make(map[string]time.Time)
	items := make([]domain.NewsItem, 0, len(parsed))
	for _, p := range parsed {
		item := p.item
		if !p.dated {
			first, ok := a.firstSeen[item.ID]
			if !ok {
				first = now
			}
			seen[item.ID] = first
			item.PublishedAt = first
		}
		items = append(items, item)
	}
	a.firstSeen = seen
	return items
}

// download performs the HTTP GET for the feed and returns the body.
func (a *RSSAdapter) download(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", RSSUserAgent)
	resp, err := clientOrDefault(a.Client, rssTimeout).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFeedBodySize))
}

// RSSAdapterFromSpec parses a 'Name::URL' spec into an adapter, or returns nil if invalid.
// Only the first '::' separates name from URL.
func RSSAdapterFromSpec(spec string) *RSSAdapter {
	name, url, found := strings.Cut(spec, "::")
	if !found {
		log.Warn(fmt.Sprintf("Invalid NEWS_SOURCES entry (expected 'Name::URL'): %q", spec))
		return nil
	}
	name, url = strings.TrimSpace(name), strings.TrimSpace(url)
	if name == "" || url == "" {
		log.Warn(fmt.Sprintf("Invalid NEWS_SOURCES entry — name or URL is empty: %q", spec))
		return nil
	}
	return NewRSSAdapter(name, url)
}

// BuildRSSAdapters builds adapters from 'Name::URL' specs, skipping invalid ones.
func BuildRSSAdapters(specs []string) []*RSSAdapter {
	adapters := make([]*RSSAdapter, 0, len(specs))
	for _, spec := range specs {
		if adapter := RSSAdapterFromSpec(spec); adapter != nil {
			adapters = append(adapters, adapter)
		}
	}
	return adapters
}

// rssChild is one direct child element of an RSS <item> or Atom <entry>.
type rssChild struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Text    string     `xml:",chardata"`
}

// attr returns the child's attribute named local, or "".
func (c rssChild) attr(local string) string {
	for _, a := range c.Attrs {
		if a.Name.Local == local {
			return strings.TrimSpace(a.Value)
		}
	}
	return ""
}

// rssItem captures every direct child of an RSS <item> or Atom <entry> so
// fields can be matched by name.
type rssItem struct {
	XMLName  xml.Name
	Children []rssChild `xml:",any"`
}

// text returns the first child text named local in the item's own namespace
// (so <title> matches but <dc:title> or <atom:link> does not).
func (it rssItem) text(local string) string {
	for _, child := range it.Children {
		if child.XMLName.Local == local && child.XMLName.Space == it.XMLName.Space {
			return strings.TrimSpace(child.Text)
		}
	}
	return ""
}

// firstText returns the first non-empty child text among locals, in order.
func (it rssItem) firstText(locals ...string) string {
	for _, local := range locals {
		if v := it.text(local); v != "" {
			return v
		}
	}
	return ""
}

// link returns the item's page address: the RSS <link> text, or else the
// href of an Atom <link> whose rel is empty or "alternate" (so enclosure
// and self links are ignored).
func (it rssItem) link() string {
	if v := it.text("link"); v != "" {
		return v
	}
	for _, child := range it.Children {
		if child.XMLName.Local != "link" || child.XMLName.Space != it.XMLName.Space {
			continue
		}
		if rel := child.attr("rel"); rel == "" || rel == "alternate" {
			return child.attr("href")
		}
	}
	return ""
}

// parsedItem is a feed item plus whether its pubDate could be parsed.
type parsedItem struct {
	item  domain.NewsItem
	dated bool
}

// ParseRSS parses up to maxItems <item> elements from an RSS document, or
// <entry> elements from an Atom one (summary or content stands in for the
// description, published or updated for the pubDate). Title
// and description are cleaned to plain single-line text and capped (see
// CleanText); only http(s) links are kept. Items without a title are
// skipped; unparseable pubDates fall back to now (UTC).
func ParseRSS(data []byte, sourceName string, maxItems int, now time.Time) ([]domain.NewsItem, error) {
	parsed, err := parseRSSItems(data, sourceName, maxItems)
	if err != nil {
		return nil, err
	}
	items := make([]domain.NewsItem, 0, len(parsed))
	for _, p := range parsed {
		if !p.dated {
			p.item.PublishedAt = NormalisePublishedAt(now)
		}
		items = append(items, p.item)
	}
	return items, nil
}

// parseRSSItems does the work of ParseRSS, leaving undated items' time zero.
func parseRSSItems(data []byte, sourceName string, maxItems int) ([]parsedItem, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false // tolerate HTML entities such as &nbsp; common in feeds
	decoder.Entity = xml.HTMLEntity
	// No HTMLAutoClose: RSS <link> carries text and must not be treated as a void element.
	decoder.CharsetReader = charsetReader

	items := []parsedItem{}
	seen := 0
	for seen < maxItems {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse RSS: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || (start.Name.Local != "item" && start.Name.Local != "entry") {
			continue
		}
		var raw rssItem
		if err := decoder.DecodeElement(&raw, &start); err != nil {
			return nil, fmt.Errorf("parse RSS item: %w", err)
		}
		seen++

		// Feed text is untrusted and ends up in LLM prompts: reduce it to plain,
		// single-line, length-capped text before anything else sees it.
		title := CleanText(raw.text("title"), MaxTitleRunes)
		if title == "" {
			continue
		}
		link := safeLink(raw.link())
		published, dated := parseDate(raw.firstText("pubDate", "published", "updated"))
		items = append(items, parsedItem{
			item: domain.NewsItem{
				ID:          StableID(title, link),
				Source:      sourceName,
				Title:       title,
				Content:     CleanText(raw.firstText("description", "summary", "content"), MaxDescriptionRunes),
				PublishedAt: published,
				URL:         link,
			},
			dated: dated,
		})
	}
	return items, nil
}

// usZoneOffsets maps the US zone abbreviations feeds use to numeric offsets;
// Go's parser would otherwise read them as UTC.
var usZoneOffsets = map[string]string{
	"EST": "-0500", "EDT": "-0400",
	"CST": "-0600", "CDT": "-0500",
	"MST": "-0700", "MDT": "-0600",
	"PST": "-0800", "PDT": "-0700",
}

// parsePubDate parses an RFC 822/1123 (or RFC 3339) date to UTC, defaulting to now.
func parsePubDate(value string, now time.Time) time.Time {
	if t, ok := parseDate(value); ok {
		return t
	}
	return NormalisePublishedAt(now)
}

// parseDate parses an RFC 822/1123 (or RFC 3339) date to UTC and reports
// whether it succeeded. A trailing US zone abbreviation is replaced by its
// offset first.
func parseDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if fields := strings.Fields(value); len(fields) > 1 {
		if offset, ok := usZoneOffsets[strings.ToUpper(fields[len(fields)-1])]; ok {
			fields[len(fields)-1] = offset
			value = strings.Join(fields, " ")
		}
	}
	if t, err := mail.ParseDate(value); err == nil {
		return NormalisePublishedAt(t), true
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return NormalisePublishedAt(t), true
	}
	return time.Time{}, false
}

// charsetReader decodes the non-UTF-8 encodings feeds commonly declare.
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return input, nil
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1":
		return latin1Reader(input)
	default:
		return nil, fmt.Errorf("unsupported feed charset %q", charset)
	}
}

// latin1Reader converts ISO-8859-1 bytes (each byte is its own code point) to UTF-8.
func latin1Reader(input io.Reader) (io.Reader, error) {
	raw, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, c := range raw {
		b.WriteRune(rune(c))
	}
	return strings.NewReader(b.String()), nil
}
