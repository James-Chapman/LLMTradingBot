// Package news provides news ingestion adapters: RSS and Atom feeds (the
// built-in catalogue in config.BuiltInNewsFeeds plus extra
// 'Name::URL' feeds) and the Alternative.me Crypto Fear & Greed index.
//
// Article IDs are derived from sha256(title + url) so they are stable across
// restarts and de-duplication of already-briefed items keeps working.
package news

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

// log is the package logger (logger="trading_bot.news_adapter").
var log = observability.Logger("news_adapter")

// Adapter is a source of news items.
type Adapter interface {
	// SourceName returns the human-readable source name.
	SourceName() string
	// FetchNews returns the latest items; on failure it logs, returns an empty slice and the error.
	FetchNews(ctx context.Context) ([]domain.NewsItem, error)
}

// StableID returns a 16-char hex ID derived from title and url.
func StableID(title, url string) string {
	sum := sha256.Sum256([]byte(title + url))
	return hex.EncodeToString(sum[:])[:16]
}

// NormalisePublishedAt returns t as UTC so mixed-source items sort safely.
func NormalisePublishedAt(t time.Time) time.Time {
	return t.UTC()
}

// NormaliseNewsItem returns a copy of item whose PublishedAt is UTC.
func NormaliseNewsItem(item domain.NewsItem) domain.NewsItem {
	item.PublishedAt = NormalisePublishedAt(item.PublishedAt)
	return item
}

// DefaultSignal returns the neutral signal used before any analysis of item.
func DefaultSignal(item domain.NewsItem) domain.NewsSignal {
	return domain.NewsSignal{
		NewsItemID:        item.ID,
		AssetMentions:     []string{},
		HeadlineSentiment: 0,
		Confidence:        0.5,
	}
}

// clientOrDefault returns client, or a new client with timeout if nil.
func clientOrDefault(client *http.Client, timeout time.Duration) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: timeout}
}
