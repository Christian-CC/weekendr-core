package weekendr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// archiveDelayDays is roughly how far back Open-Meteo's archive API lags real
// time. Anything more recent must come from the forecast API, which serves
// `past_days` of recent observations.
const archiveDelayDays = 6

type openMeteoDailyResponse struct {
	Daily struct {
		Time             []string   `json:"time"`
		Temperature2mMax []*float64 `json:"temperature_2m_max"`
		WeatherCode      []*int     `json:"weather_code"`
	} `json:"daily"`
}

// parseTakenAt parses the TakenAt string carried in a PhotoIndexEntry. iOS
// writes it via ISO8601DateFormatter (RFC3339, e.g. "2025-04-16T10:00:00Z"),
// but be lenient about a few other shapes (EXIF DateTimeOriginal
// "2006:01:02 15:04:05", or a bare date) just in case.
func parseTakenAt(s string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006:01:02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// fetchWeather returns the daily max temperature (°C) and WMO weather code for
// the given location and date. It uses the forecast endpoint for recent dates
// (within archiveDelayDays) and the archive endpoint for older ones.
//
// Returns (nil, nil, nil) when the API has no data for that day; an error is
// returned only on transport / decoding / HTTP-status failures.
func fetchWeather(lat, lon float64, date time.Time) (*float64, *int, error) {
	dateStr := date.Format("2006-01-02")
	ageDays := int(time.Since(date).Hours() / 24)

	var url string
	if ageDays < archiveDelayDays {
		pastDays := ageDays + 1
		if pastDays < 1 {
			pastDays = 1
		}
		if pastDays > 92 {
			pastDays = 92 // Open-Meteo caps past_days at 92
		}
		url = fmt.Sprintf(
			"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&past_days=%d&forecast_days=1&daily=temperature_2m_max,weather_code&timezone=auto",
			lat, lon, pastDays,
		)
	} else {
		url = fmt.Sprintf(
			"https://archive-api.open-meteo.com/v1/archive?latitude=%.4f&longitude=%.4f&start_date=%s&end_date=%s&daily=temperature_2m_max,weather_code&timezone=auto",
			lat, lon, dateStr, dateStr,
		)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("open-meteo HTTP %d", resp.StatusCode)
	}

	var result openMeteoDailyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil, err
	}

	// Locate the entry for the requested date. The archive single-day query
	// returns exactly one element; the forecast query returns past_days+1
	// elements, so match on the date string.
	idx := -1
	for i, d := range result.Daily.Time {
		if d == dateStr {
			idx = i
			break
		}
	}
	if idx < 0 {
		if len(result.Daily.Time) == 0 && len(result.Daily.Temperature2mMax) == 1 {
			idx = 0 // tolerate a response without a "time" array
		} else {
			return nil, nil, nil
		}
	}
	if idx >= len(result.Daily.Temperature2mMax) || idx >= len(result.Daily.WeatherCode) {
		return nil, nil, nil
	}

	temp := result.Daily.Temperature2mMax[idx]
	code := result.Daily.WeatherCode[idx]
	if temp == nil || code == nil {
		return nil, nil, nil
	}
	// Copy out of the slice so callers don't retain the decoded buffer.
	t, c := *temp, *code
	return &t, &c, nil
}
