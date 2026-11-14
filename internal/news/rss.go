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
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

const (
	// RSSUserAgent identifies the bot to feed servers.
	RSSUserAgent = "KrakenBot/0.1 (RSS reader; personal use)"
	// DefaultMaxItems is how many feed items are considered per fetch.
	DefaultMaxItems = 20

	rssTimeout      = 15 * time.Second
	maxFeedBodySize = 10 << 20 // 10 MiB guard against runaway responses
)

// RSSAdapter fetches one RSS feed. Build it with NewRSSAdapter or RSSAdapterFromSpec.
type RSSAdapter struct {
	Name     string
	URL      string
	Client   *http.Client // nil: default client with a 15 s timeout
	MaxItems int          // <= 0: DefaultMaxItems
}

// NewRSSAdapter returns an adapter for the feed at url labelled name.
func NewRSSAdapter(name, url string) *RSSAdapter {
	return &RSSAdapter{Name: name, URL: url}
}

// SourceName returns the feed's configured name.
func (a *RSSAdapter) SourceName() string {
	return a.Name
}

// FetchNews downloads and parses the feed.
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
	items, err := ParseRSS(body, a.Name, maxItems, domain.NowUTC())
	if err != nil {
		log.Warn(a.Name+" RSS parse failed", "error", err.Error())
		return []domain.NewsItem{}, err
	}
	log.Info(fmt.Sprintf("Fetched %d items from %s", len(items), a.Name))
	return items, nil
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

// rssChild is one direct child element of an <item>.
type rssChild struct {
	XMLName xml.Name
	Text    string `xml:",chardata"`
}

// rssItem captures every direct child of an <item> so fields can be matched by name.
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

// ParseRSS parses up to maxItems <item> elements from an RSS document. Items
// without a title are skipped; unparseable pubDates fall back to now (UTC).
func ParseRSS(data []byte, sourceName string, maxItems int, now time.Time) ([]domain.NewsItem, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false // tolerate HTML entities such as &nbsp; common in feeds
	decoder.Entity = xml.HTMLEntity
	// No HTMLAutoClose: RSS <link> carries text and must not be treated as a void element.
	decoder.CharsetReader = charsetReader

	items := []domain.NewsItem{}
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
		if !ok || start.Name.Local != "item" {
			continue
		}
		var raw rssItem
		if err := decoder.DecodeElement(&raw, &start); err != nil {
			return nil, fmt.Errorf("parse RSS item: %w", err)
		}
		seen++

		title := raw.text("title")
		if title == "" {
			continue
		}
		link := raw.text("link")
		items = append(items, domain.NewsItem{
			ID:          StableID(title, link),
			Source:      sourceName,
			Title:       title,
			Content:     raw.text("description"),
			PublishedAt: parsePubDate(raw.text("pubDate"), now),
			URL:         link,
		})
	}
	return items, nil
}

// parsePubDate parses an RFC 822/1123 (or RFC 3339) date to UTC, defaulting to now.
func parsePubDate(value string, now time.Time) time.Time {
	if value != "" {
		if t, err := mail.ParseDate(value); err == nil {
			return NormalisePublishedAt(t)
		}
		if t, err := time.Parse(time.RFC3339, value); err == nil {
			return NormalisePublishedAt(t)
		}
	}
	return NormalisePublishedAt(now)
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
