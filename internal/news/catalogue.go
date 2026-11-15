package news

import (
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

// BuildAdapters returns one adapter per built-in source, in catalogue order,
// followed by the extra 'Name::URL' feeds. Invalid extra specs, and extras
// named like a built-in source, are skipped. Whether a source is switched
// off is decided per fetch by the caller, so every source gets an adapter.
func BuildAdapters(builtIn []config.NewsFeed, extra []string) []Adapter {
	adapters := make([]Adapter, 0, len(builtIn)+len(extra))
	names := map[string]bool{}
	for _, feed := range builtIn {
		names[strings.ToLower(feed.Name)] = true
		if feed.Kind == config.NewsKindFearGreed {
			adapters = append(adapters, NewFearGreedAdapter())
			continue
		}
		adapters = append(adapters, NewRSSAdapter(feed.Name, feed.URL))
	}
	for _, rss := range BuildRSSAdapters(extra) {
		if names[strings.ToLower(rss.Name)] {
			continue
		}
		names[strings.ToLower(rss.Name)] = true
		adapters = append(adapters, rss)
	}
	return adapters
}
