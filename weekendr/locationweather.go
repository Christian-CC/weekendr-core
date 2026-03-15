cat > ~/src/weekendr-core/weekendr/locationweather.go << 'EOF'
package weekendr

// DayContext holds the location and weather context for a single event day.
type DayContext struct {
	Date        string
	PlaceName   string
	WeatherIcon string
	Temperature float64
	Condition   string
}

// StartLocationWeather begins sampling location and weather for the
// given event, writing per-day context to the meta-folder.
func (c *Client) StartLocationWeather(eventID string) error {
	return nil
}

// StopLocationWeather stops location and weather sampling.
func (c *Client) StopLocationWeather(eventID string) error {
	return nil
}

// GetDayContext returns the location and weather context for a specific day.
func (c *Client) GetDayContext(eventID string, date string) (*DayContext, error) {
	return &DayContext{
		Date:        date,
		PlaceName:   "Stub Location",
		WeatherIcon: "partly_cloudy",
		Temperature: 22.0,
		Condition:   "Partly cloudy",
	}, nil
}
EOF