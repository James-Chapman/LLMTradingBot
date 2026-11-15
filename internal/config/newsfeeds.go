package config

import (
	"fmt"
	"slices"
	"strings"
)

// News source kinds: a feed is read by the RSS/Atom adapter, the Fear &
// Greed index by its own adapter.
const (
	NewsKindFeed      = "feed"
	NewsKindFearGreed = "fear_greed"
)

// FearGreedFeedName is the Fear & Greed index's source name; it must match
// news.FearGreedSource so the dashboard switch turns that adapter off.
const FearGreedFeedName = "Fear & Greed"

// News source categories, in the order the dashboard lists them.
const (
	NewsCategoryCrypto    = "Crypto news"
	NewsCategoryMacro     = "Macro & regulatory"
	NewsCategorySentiment = "Market sentiment"
)

// NewsFeed is one built-in news source. Every built-in source is on unless
// its name is in DISABLED_NEWS_SOURCES.
type NewsFeed struct {
	Name     string `json:"name"`
	URL      string `json:"url"` // the feed (or, for Fear & Greed, its web page)
	Category string `json:"category"`
	Kind     string `json:"kind"`
}

// builtInNewsFeeds is the catalogue. Every URL answered with RSS or Atom on
// 2026-11-15 using the bot's User-Agent.
var builtInNewsFeeds = []NewsFeed{
	{"CoinDesk", "https://www.coindesk.com/arc/outboundfeeds/rss/", NewsCategoryCrypto, NewsKindFeed},
	{"CoinTelegraph", "https://cointelegraph.com/rss", NewsCategoryCrypto, NewsKindFeed},
	{"The Block", "https://www.theblock.co/rss.xml", NewsCategoryCrypto, NewsKindFeed},
	{"Decrypt", "https://decrypt.co/feed", NewsCategoryCrypto, NewsKindFeed},
	{"Bitcoin Magazine", "https://bitcoinmagazine.com/.rss/full/", NewsCategoryCrypto, NewsKindFeed},
	{"CryptoSlate", "https://cryptoslate.com/feed/", NewsCategoryCrypto, NewsKindFeed},
	{"The Defiant", "https://thedefiant.io/feed/", NewsCategoryCrypto, NewsKindFeed},
	{"CryptoPotato", "https://cryptopotato.com/feed/", NewsCategoryCrypto, NewsKindFeed},
	{"NewsBTC", "https://www.newsbtc.com/feed/", NewsCategoryCrypto, NewsKindFeed},
	{"CryptoBriefing", "https://cryptobriefing.com/feed/", NewsCategoryCrypto, NewsKindFeed},
	{"BeInCrypto", "https://beincrypto.com/feed/", NewsCategoryCrypto, NewsKindFeed},
	{"CoinGape", "https://coingape.com/feed/", NewsCategoryCrypto, NewsKindFeed},
	{"crypto.news", "https://crypto.news/feed/", NewsCategoryCrypto, NewsKindFeed},
	{"The Daily Hodl", "https://dailyhodl.com/feed/", NewsCategoryCrypto, NewsKindFeed},
	{"AMBCrypto", "https://ambcrypto.com/feed/", NewsCategoryCrypto, NewsKindFeed},
	{"Bitcoin.com News", "https://news.bitcoin.com/feed/", NewsCategoryCrypto, NewsKindFeed},
	{"Blockworks", "https://blockworks.co/feed", NewsCategoryCrypto, NewsKindFeed}, // Atom
	{"Federal Reserve", "https://www.federalreserve.gov/feeds/press_all.xml", NewsCategoryMacro, NewsKindFeed},
	{"SEC", "https://www.sec.gov/news/pressreleases.rss", NewsCategoryMacro, NewsKindFeed},
	{"ECB", "https://www.ecb.europa.eu/rss/press.html", NewsCategoryMacro, NewsKindFeed},
	{FearGreedFeedName, "https://alternative.me/crypto/fear-and-greed-index/", NewsCategorySentiment, NewsKindFearGreed},
}

// BuiltInNewsFeeds returns a copy of the built-in news source catalogue.
func BuiltInNewsFeeds() []NewsFeed {
	return slices.Clone(builtInNewsFeeds)
}

// isBuiltInNewsName reports whether name (any case) is a built-in source.
func isBuiltInNewsName(name string) bool {
	return slices.ContainsFunc(builtInNewsFeeds, func(f NewsFeed) bool { return strings.EqualFold(f.Name, name) })
}

// extraNewsFeeds validates NEWS_SOURCES entries ('Name::URL' with an http(s)
// URL) and drops the ones named like a built-in feed: lists saved before the
// catalogue existed held the old defaults, which are now built in.
func extraNewsFeeds(specs []string) ([]string, error) {
	out := make([]string, 0, len(specs))
	names := map[string]bool{}
	for _, spec := range specs {
		name, url, found := strings.Cut(spec, "::")
		name, url = strings.TrimSpace(name), strings.TrimSpace(url)
		if !found || name == "" {
			return nil, fmt.Errorf("%q is not Name::URL", spec)
		}
		if err := httpURL(false)(url); err != nil {
			return nil, fmt.Errorf("%q: the URL %w", spec, err)
		}
		if isBuiltInNewsName(name) {
			continue
		}
		key := strings.ToLower(name)
		if names[key] {
			return nil, fmt.Errorf("two feeds are named %q", name)
		}
		names[key] = true
		out = append(out, name+"::"+url)
	}
	return out, nil
}

// newsNames trims the switched-off source names and drops blanks and repeats.
func newsNames(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}
