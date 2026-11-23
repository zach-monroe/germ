package widgets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// weather shows current conditions and a short forecast from Open-Meteo
// (no API key). Options: location (city name; default: from your IP
// address), units ("fahrenheit" or "celsius"; default by country).
type weather struct {
	mu    sync.Mutex
	place string
	lat   float64
	lon   float64
	units string
	data  *forecast
}

type forecast struct {
	Current struct {
		Temp     float64 `json:"temperature_2m"`
		Feels    float64 `json:"apparent_temperature"`
		Humidity float64 `json:"relative_humidity_2m"`
		Code     int     `json:"weather_code"`
		Wind     float64 `json:"wind_speed_10m"`
		IsDay    int     `json:"is_day"`
	} `json:"current"`
	Daily struct {
		Time []string  `json:"time"`
		Code []int     `json:"weather_code"`
		Max  []float64 `json:"temperature_2m_max"`
		Min  []float64 `json:"temperature_2m_min"`
	} `json:"daily"`
}

func newWeather() Widget { return &weather{units: Option("units", "")} }

func (w *weather) Every() time.Duration { return 15 * time.Minute }
func (w *weather) Tick() time.Duration  { return 0 }

func (w *weather) Update(ctx context.Context) error {
	w.mu.Lock()
	located := w.place != ""
	w.mu.Unlock()
	if !located {
		if err := w.locate(ctx); err != nil {
			return err
		}
	}
	w.mu.Lock()
	lat, lon, units := w.lat, w.lon, w.units
	w.mu.Unlock()
	speed := "kmh"
	if units == "fahrenheit" {
		speed = "mph"
	}
	u := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f"+
		"&current=temperature_2m,apparent_temperature,relative_humidity_2m,weather_code,wind_speed_10m,is_day"+
		"&daily=weather_code,temperature_2m_max,temperature_2m_min&timezone=auto&forecast_days=6"+
		"&temperature_unit=%s&wind_speed_unit=%s", lat, lon, units, speed)
	body, err := get(ctx, u)
	if err != nil {
		return err
	}
	var f forecast
	if err := json.Unmarshal(body, &f); err != nil {
		return err
	}
	w.mu.Lock()
	w.data = &f
	w.mu.Unlock()
	return nil
}

func (w *weather) locate(ctx context.Context) error {
	country := ""
	if name := Option("location", ""); name != "" {
		body, err := get(ctx, "https://geocoding-api.open-meteo.com/v1/search?count=1&name="+url.QueryEscape(name))
		if err != nil {
			return err
		}
		var r struct {
			Results []struct {
				Name        string  `json:"name"`
				Admin1      string  `json:"admin1"`
				CountryCode string  `json:"country_code"`
				Lat         float64 `json:"latitude"`
				Lon         float64 `json:"longitude"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return err
		}
		if len(r.Results) == 0 {
			return fmt.Errorf("location %q not found", name)
		}
		g := r.Results[0]
		w.mu.Lock()
		w.place, w.lat, w.lon = joinNonEmpty(g.Name, g.Admin1), g.Lat, g.Lon
		w.mu.Unlock()
		country = g.CountryCode
	} else {
		r, err := ipLocation(ctx)
		if err != nil {
			return fmt.Errorf("couldn't find your location; set location in [widgets.weather]")
		}
		w.mu.Lock()
		w.place, w.lat, w.lon = joinNonEmpty(r.City, r.Region), r.Lat, r.Lon
		w.mu.Unlock()
		country = r.Country
	}
	w.mu.Lock()
	if w.units == "" {
		w.units = "celsius"
		switch country {
		case "US", "LR", "MM", "BS", "BZ", "KY", "PW":
			w.units = "fahrenheit"
		}
	}
	w.mu.Unlock()
	return nil
}

type ipLoc struct {
	City, Region, Country string
	Lat, Lon              float64
}

// ipLocation estimates the location from the public IP address, trying
// two free services.
func ipLocation(ctx context.Context) (ipLoc, error) {
	var a struct {
		City    string  `json:"city"`
		Region  string  `json:"region_code"`
		Country string  `json:"country_code"`
		Lat     float64 `json:"latitude"`
		Lon     float64 `json:"longitude"`
	}
	if body, err := get(ctx, "https://ipapi.co/json/"); err == nil && json.Unmarshal(body, &a) == nil && (a.Lat != 0 || a.Lon != 0) {
		return ipLoc{a.City, a.Region, a.Country, a.Lat, a.Lon}, nil
	}
	var b struct {
		City    string  `json:"city"`
		Region  string  `json:"region"`
		Country string  `json:"countryCode"`
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
	}
	body, err := get(ctx, "http://ip-api.com/json")
	if err == nil {
		err = json.Unmarshal(body, &b)
	}
	if err != nil || b.Lat == 0 && b.Lon == 0 {
		return ipLoc{}, fmt.Errorf("no location")
	}
	return ipLoc{b.City, b.Region, b.Country, b.Lat, b.Lon}, nil
}

func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

// WMO weather codes -> (description, art kind).
func describe(code int) (string, string) {
	switch {
	case code == 0:
		return "Clear", "sun"
	case code == 1:
		return "Mostly clear", "sun"
	case code == 2:
		return "Partly cloudy", "partly"
	case code == 3:
		return "Overcast", "cloud"
	case code == 45 || code == 48:
		return "Fog", "fog"
	case code >= 51 && code <= 57:
		return "Drizzle", "rain"
	case code >= 61 && code <= 67, code >= 80 && code <= 82:
		return "Rain", "rain"
	case code >= 71 && code <= 77, code == 85 || code == 86:
		return "Snow", "snow"
	case code >= 95:
		return "Thunderstorm", "storm"
	}
	return "Unknown", "cloud"
}

// art is a small colored picture per condition (5 rows × 13 cells).
func art(kind string, day bool) []string {
	sun := yellow
	if !day {
		sun = white // moon
	}
	c := white
	switch kind {
	case "sun":
		if !day {
			return []string{
				sun + "     _.._    ",
				sun + "   .' .-'`   ",
				sun + "  /  /       ",
				sun + "  |  |       ",
				sun + "   \\  '.___.'",
			}
		}
		return []string{
			sun + "    \\   /    ",
			sun + "     .-.     ",
			sun + "  ― (   ) ―  ",
			sun + "     `-'     ",
			sun + "    /   \\    ",
		}
	case "partly":
		return []string{
			sun + "   \\  /      ",
			sun + " _ /\"\"" + c + ".-.    ",
			sun + "   \\_" + c + "(   ).  ",
			sun + "   /" + c + "(___(__) ",
			"             ",
		}
	case "rain":
		return []string{
			c + "     .-.     ",
			c + "    (   ).   ",
			c + "   (___(__)  ",
			blue + "    ‚ʻ‚ʻ‚ʻ‚ʻ  ",
			blue + "    ‚ʻ‚ʻ‚ʻ‚ʻ  ",
		}
	case "snow":
		return []string{
			c + "     .-.     ",
			c + "    (   ).   ",
			c + "   (___(__)  ",
			bright + "    *  *  *  ",
			bright + "   *  *  *   ",
		}
	case "storm":
		return []string{
			c + "     .-.     ",
			c + "    (   ).   ",
			c + "   (___(__)  ",
			yellow + "    ⚡" + blue + "ʻʻ" + yellow + "⚡" + blue + "ʻʻ  ",
			blue + "    ʻ‚ʻ‚ʻ‚   ",
		}
	case "fog":
		return []string{
			"             ",
			dim + c + " _ - _ - _ - ",
			dim + c + "  _ - _ - _  ",
			dim + c + " _ - _ - _ - ",
			"             ",
		}
	}
	return []string{
		"             ",
		c + "     .--.    ",
		c + "  .-(    ).  ",
		c + " (___.__)__) ",
		"             ",
	}
}

func (w *weather) Draw(wd, h int, err error) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	lines := []string{title(wd, "☁", "weather", truncate(w.place, max(0, wd-14)))}
	if w.data == nil {
		if err == nil {
			lines = append(lines, "", center(dim+"loading…"+reset, wd))
		}
		return fill(lines, h, errLine(err, wd))
	}
	d := w.data
	unit := "°C"
	speed := "km/h"
	if w.units == "fahrenheit" {
		unit, speed = "°F", "mph"
	}
	desc, kind := describe(d.Current.Code)
	pic := art(kind, d.Current.IsDay == 1)
	info := []string{
		bold + bright + fmt.Sprintf("%.0f%s", d.Current.Temp, unit) + reset + "  " + desc,
		dim + "feels like " + reset + fmt.Sprintf("%.0f%s", d.Current.Feels, unit),
		dim + "humidity   " + reset + fmt.Sprintf("%.0f%%", d.Current.Humidity),
		dim + "wind       " + reset + fmt.Sprintf("%.0f %s", d.Current.Wind, speed),
	}
	if len(d.Daily.Max) > 0 {
		info = append(info, dim+"today      "+reset+fmt.Sprintf("%s%.0f°%s / %s%.0f°%s",
			red, d.Daily.Max[0], reset, blue, d.Daily.Min[0], reset))
	}
	lines = append(lines, "")
	for i := 0; i < 5; i++ {
		row := " " + pic[i] + reset
		if wd >= 36 && i < len(info) {
			row += "  " + info[i]
		}
		lines = append(lines, row)
	}
	if wd < 36 { // narrow: info below the picture
		lines = append(lines, info...)
	}

	// Forecast strip: one column per day while it fits.
	if h-len(lines) >= 4 && len(d.Daily.Time) > 1 {
		const colW = 10
		n := min(len(d.Daily.Time)-1, max(1, (wd-1)/colW))
		var day, ico, hi strings.Builder
		day.WriteString(" ")
		ico.WriteString(" ")
		hi.WriteString(" ")
		for i := 1; i <= n; i++ {
			t, _ := time.Parse("2006-01-02", d.Daily.Time[i])
			_, k := describe(d.Daily.Code[i])
			pad := func(s string) string { return s + strings.Repeat(" ", max(0, colW-width(s))) }
			day.WriteString(pad(bold + t.Format("Mon") + reset))
			ico.WriteString(pad(smallIcon(k)))
			hi.WriteString(pad(fmt.Sprintf("%s%.0f°%s %s%.0f°%s", red, d.Daily.Max[i], reset, blue, d.Daily.Min[i], reset)))
		}
		lines = append(lines, "", day.String(), ico.String(), hi.String())
	}
	return fill(lines, h, errLine(err, wd))
}

func smallIcon(kind string) string {
	switch kind {
	case "sun":
		return yellow + "☀ sun" + reset
	case "partly":
		return yellow + "⛅ part" + reset
	case "rain":
		return blue + "☂ rain" + reset
	case "snow":
		return bright + "❄ snow" + reset
	case "storm":
		return yellow + "⚡ storm" + reset
	case "fog":
		return dim + "≡ fog" + reset
	}
	return white + "☁ cloud" + reset
}
