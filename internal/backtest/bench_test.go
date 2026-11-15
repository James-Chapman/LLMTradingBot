package backtest

import (
	"context"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// BenchmarkReplay measures replay throughput with the real basic strategy
// over three days of 30 s random-walk bars for three markets.
func BenchmarkReplay(b *testing.B) {
	const ticks = 3 * 2880
	reader := memReader{bars: map[string][]histdata.Bar{
		"BTC/EUR": randomWalkBars(1, ticks+1, t0, 50000, 0.002),
		"ETH/EUR": randomWalkBars(2, ticks+1, t0, 3000, 0.003),
		"SOL/EUR": randomWalkBars(3, ticks+1, t0, 150, 0.004),
	}}
	cfg := testConfig([]string{"BTC/EUR", "ETH/EUR", "SOL/EUR"}, ticks)
	b.ResetTimer()
	for range b.N {
		if _, err := Run(context.Background(), reader, cfg); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(ticks*b.N)/b.Elapsed().Seconds(), "ticks/s")
}
