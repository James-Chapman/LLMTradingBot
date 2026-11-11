package storage

import (
	"context"
	"testing"
	"time"
)

// TestDatetimeHandling covers SQLAlchemy-compatible datetime storage (and the
// storage parts of tests/test_datetime_utc_bdd.py).
func TestDatetimeHandling(t *testing.T) {
	ctx := context.Background()
	want := time.Date(2026, 4, 30, 12, 34, 56, 123456000, time.UTC)

	t.Run("GIVEN SQLAlchemy and RFC 3339 datetime strings WHEN parsed THEN all yield the same UTC instant", func(t *testing.T) {
		for _, s := range []string{
			"2026-04-30 12:34:56.123456",
			"2026-04-30 12:34:56.123456+00:00",
			"2026-04-30T12:34:56.123456",
			"2026-04-30T12:34:56.123456Z",
			"2026-04-30T14:34:56.123456+02:00",
		} {
			got, err := parseDBTime(s)
			if err != nil {
				t.Errorf("parseDBTime(%q) failed: %v", s, err)
				continue
			}
			if !got.Equal(want) || got.Location() != time.UTC {
				t.Errorf("parseDBTime(%q) = %v, want %v in UTC", s, got, want)
			}
		}
		if _, err := parseDBTime("2026-04-30 12:34:56"); err != nil {
			t.Errorf("CURRENT_TIMESTAMP format should parse: %v", err)
		}
	})

	t.Run("GIVEN a non-UTC time WHEN formatted for the DB THEN it is written in SQLAlchemy's UTC layout", func(t *testing.T) {
		local := want.In(time.FixedZone("CEST", 2*3600))
		if got := formatDBTime(local); got != "2026-04-30 12:34:56.123456" {
			t.Errorf("formatDBTime = %q", got)
		}
		if got := formatDBTime(time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)); got != "2026-04-30 12:00:00.000000" {
			t.Errorf("zero microseconds must still be fixed-width, got %q", got)
		}
	})

	t.Run("GIVEN a time WHEN rendered for the API THEN it matches Python's naive isoformat", func(t *testing.T) {
		if got := pyISO(want); got != "2026-04-30T12:34:56.123456" {
			t.Errorf("pyISO = %q", got)
		}
		if got := pyISO(time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)); got != "2026-04-30T12:00:00" {
			t.Errorf("pyISO without microseconds = %q", got)
		}
	})

	t.Run("GIVEN rows written by SQLAlchemy WHEN read by the repository THEN their datetimes are parsed", func(t *testing.T) {
		repo := newTestRepo(t)
		_, err := repo.db.Exec(`INSERT INTO risk_rejections (market, direction, confidence, thesis, reason, timestamp)
			VALUES ('BTC/EUR', 'long', 0.5, '', 'naive', '2026-04-30 12:34:56.123456'),
			       ('ETH/EUR', 'long', 0.5, '', 'offset', '2026-04-30 11:00:00.000000+00:00')`)
		mustNoErr(t, err)
		_, err = repo.db.Exec(`INSERT INTO activity_log (level, message, detail, timestamp)
			VALUES ('info', 'from python', NULL, '2026-04-30 12:34:56.123456')`)
		mustNoErr(t, err)

		rejections, err := repo.GetRecentRiskRejections(ctx, 10)
		mustNoErr(t, err)
		if len(rejections) != 2 || rejections[0].Timestamp != "2026-04-30T12:34:56.123456" || rejections[1].Timestamp != "2026-04-30T11:00:00" {
			t.Errorf("unexpected timestamps: %+v", rejections)
		}

		activity, err := repo.GetRecentActivity(10)
		mustNoErr(t, err)
		if len(activity) != 1 || activity[0].Detail != "" {
			t.Fatalf("unexpected activity: %+v", activity)
		}
		got, err := time.Parse(time.RFC3339Nano, activity[0].Timestamp)
		mustNoErr(t, err)
		if !got.Equal(want) {
			t.Errorf("activity timestamp %v, want %v", got, want)
		}
	})

	t.Run("GIVEN a datetime written by the repository WHEN read raw THEN it uses SQLAlchemy's text layout", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveActivityLog("info", "go", ""))

		var raw string
		mustNoErr(t, repo.db.QueryRow("SELECT CAST(timestamp AS TEXT) FROM activity_log").Scan(&raw))
		if _, err := time.Parse(dbTimeLayout, raw); err != nil || len(raw) != len(dbTimeLayout) {
			t.Errorf("stored timestamp %q is not in SQLAlchemy layout", raw)
		}
	})
}
