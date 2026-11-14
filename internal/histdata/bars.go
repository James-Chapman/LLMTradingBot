package histdata

// barBuilder folds time-ordered trades into 30-second bars, holding only the
// bar being built (constant memory).
type barBuilder struct {
	bar  Bar
	open bool
}

// add folds t into the current bar. When t starts a later interval, the
// finished bar is returned with ok true. sideKnown false leaves the trade out
// of both side totals (its volume still counts in Volume).
func (b *barBuilder) add(t Trade, sideKnown bool) (done Bar, ok bool) {
	start := t.Time.UTC().Truncate(BarInterval)
	if b.open && !start.Equal(b.bar.Start) {
		done, ok = b.bar, true
		b.open = false
	}
	if !b.open {
		b.bar = Bar{Start: start, Open: t.Price, High: t.Price, Low: t.Price}
		b.open = true
	}
	b.bar.High = max(b.bar.High, t.Price)
	b.bar.Low = min(b.bar.Low, t.Price)
	b.bar.Close = t.Price
	b.bar.Volume += t.Volume
	b.bar.Trades++
	if sideKnown {
		if t.Buy {
			b.bar.BuyVolume += t.Volume
			b.bar.BuyNotional += t.Price * t.Volume
		} else {
			b.bar.SellVolume += t.Volume
			b.bar.SellNotional += t.Price * t.Volume
		}
	}
	return done, ok
}

// flush returns the bar being built, if any, and resets the builder.
func (b *barBuilder) flush() (Bar, bool) {
	if !b.open {
		return Bar{}, false
	}
	b.open = false
	return b.bar, true
}
