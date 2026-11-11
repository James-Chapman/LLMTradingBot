package news

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

const (
	// FearGreedAPIURL is the Alternative.me index endpoint (latest reading only).
	FearGreedAPIURL = "https://api.alternative.me/fng/?limit=1"
	// FearGreedPageURL is linked from generated news items.
	FearGreedPageURL = "https://alternative.me/crypto/fear-and-greed-index/"
	// FearGreedSource is the adapter's source name.
	FearGreedSource = "Fear & Greed"

	fearGreedTimeout = 10 * time.Second
)

// FearGreedAdapter turns the Crypto Fear & Greed Index into news items. The
// index updates daily, so an item is produced only when the value changes.
type FearGreedAdapter struct {
	APIURL  string       // default FearGreedAPIURL
	PageURL string       // default FearGreedPageURL
	Client  *http.Client // nil: default client with a 10 s timeout

	mu        sync.Mutex
	lastValue *int
}

// NewFearGreedAdapter returns an adapter using the public Alternative.me API.
func NewFearGreedAdapter() *FearGreedAdapter {
	return &FearGreedAdapter{APIURL: FearGreedAPIURL, PageURL: FearGreedPageURL}
}

// SourceName returns "Fear & Greed".
func (a *FearGreedAdapter) SourceName() string {
	return FearGreedSource
}

// fearGreedResponse is the subset of the Alternative.me response we use.
type fearGreedResponse struct {
	Data []struct {
		Value          json.Number `json:"value"` // sent as a quoted number
		Classification string      `json:"value_classification"`
	} `json:"data"`
}

// FetchNews returns one item when the index reading changed since the last call.
func (a *FearGreedAdapter) FetchNews(ctx context.Context) ([]domain.NewsItem, error) {
	value, label, err := a.fetchReading(ctx)
	if err != nil {
		log.Warn("Fear & Greed fetch failed", "error", err.Error())
		return []domain.NewsItem{}, err
	}

	a.mu.Lock()
	unchanged := a.lastValue != nil && *a.lastValue == value
	if !unchanged {
		a.lastValue = &value
	}
	a.mu.Unlock()
	if unchanged {
		return []domain.NewsItem{}, nil
	}

	now := domain.NowUTC()
	title := fmt.Sprintf("Crypto Fear & Greed Index: %s (%d/100)", label, value)
	content := fmt.Sprintf(
		"The Crypto Fear & Greed Index is currently %d/100 — %s. %s "+
			"This reading is updated daily and reflects a composite of volatility, "+
			"market momentum, social media activity, and BTC dominance.",
		value, label, fearGreedZone(value))
	pageURL := a.PageURL
	if pageURL == "" {
		pageURL = FearGreedPageURL
	}

	log.Info(fmt.Sprintf("Fear & Greed updated: %s (%d/100)", label, value))
	return []domain.NewsItem{{
		ID:          StableID(title, now.Format("2006-01-02")), // one ID per reading per UTC day
		Source:      FearGreedSource,
		Title:       title,
		Content:     content,
		PublishedAt: now,
		URL:         pageURL,
	}}, nil
}

// fetchReading downloads the latest index value and its classification label.
func (a *FearGreedAdapter) fetchReading(ctx context.Context) (int, string, error) {
	apiURL := a.APIURL
	if apiURL == "" {
		apiURL = FearGreedAPIURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "KrakenBot/0.1")
	resp, err := clientOrDefault(a.Client, fearGreedTimeout).Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var payload fearGreedResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return 0, "", fmt.Errorf("decode response: %w", err)
	}
	if len(payload.Data) == 0 {
		return 0, "", errors.New("response has no data")
	}
	value, err := strconv.Atoi(payload.Data[0].Value.String())
	if err != nil {
		return 0, "", fmt.Errorf("parse value: %w", err)
	}
	return value, payload.Data[0].Classification, nil
}

// fearGreedZone explains what an index value means for market sentiment.
func fearGreedZone(value int) string {
	switch {
	case value <= 25:
		return "Extreme Fear — market participants are very worried; historically a buying opportunity."
	case value <= 45:
		return "Fear — sentiment is cautious; prices may be suppressed below fair value."
	case value <= 55:
		return "Neutral — balanced market sentiment with no strong directional bias."
	case value <= 75:
		return "Greed — market is optimistic; risk of overshoot or near-term correction is elevated."
	default:
		return "Extreme Greed — euphoric sentiment; historically precedes significant corrections."
	}
}
