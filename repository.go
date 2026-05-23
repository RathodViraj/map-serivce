package mapservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type OverpassRepository struct {
	client     *http.Client
	baseURL    string
	retryCount int
	retryDelay time.Duration
}

func NewRepository(client *http.Client, baseURL string, retryCount int, retryDelay time.Duration) *OverpassRepository {
	if client == nil {
		client = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	if baseURL == "" {
		baseURL = DefaultOverpassURL
	}
	if retryCount < 0 {
		retryCount = 0
	}
	if retryDelay <= 0 {
		retryDelay = DefaultRetryDelay
	}
	return &OverpassRepository{
		client:     client,
		baseURL:    baseURL,
		retryCount: retryCount,
		retryDelay: retryDelay,
	}
}

func (r *OverpassRepository) SearchNearby(ctx context.Context, query NearbyQuery) ([]NearbyPlace, error) {
	overpassQuery := buildOverpassQuery(query)
	form := url.Values{}
	form.Set("data", overpassQuery)

	log.Printf(`{"level":"info","message":"overpass request","type":%q,"radius":%d,"lat":%.6f,"lon":%.6f}`, query.Type, query.Radius, query.Latitude, query.Longitude)

	var err error
	var responseBody []byte
	for attempt := 0; attempt <= r.retryCount; attempt++ {
		responseBody, err = r.execute(ctx, form.Encode())
		if err == nil {
			break
		}
		if attempt < r.retryCount && shouldRetry(err) {
			select {
			case <-time.After(r.retryDelay * time.Duration(attempt+1)):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
		return nil, err
	}

	log.Printf(`{"level":"info","message":"overpass response received","bytes":%d}`, len(responseBody))

	var parsed overpassResponse
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		log.Printf(`{"level":"error","message":"overpass decode failed","error":%q,"body":%q}`, err.Error(), truncateLogBody(responseBody))
		return nil, fmt.Errorf("decode overpass response: %w", err)
	}

	places := make([]NearbyPlace, 0, len(parsed.Elements))
	seen := make(map[string]struct{})
	for _, element := range parsed.Elements {
		lat, lon, ok := element.coordinates()
		if !ok {
			continue
		}
		place := NearbyPlace{
			Name:      element.displayName(query.Type),
			Latitude:  lat,
			Longitude: lon,
			Category:  query.Type,
			Address:   element.addressString(),
		}
		key := strings.ToLower(fmt.Sprintf("%s:%.6f:%.6f:%s", place.Name, place.Latitude, place.Longitude, place.Category))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		places = append(places, place)
	}

	return places, nil
}

func (r *OverpassRepository) execute(ctx context.Context, body string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create overpass request: %w", err)
	}
	req.Header.Set("User-Agent", "mapservice/1.0")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute overpass request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("read overpass response: %w", readErr)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Printf(`{"level":"error","message":"overpass returned error status","status":%d,"body":%q}`, resp.StatusCode, truncateLogBody(responseBody))
		return nil, fmt.Errorf("overpass returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}

	return responseBody, nil
}

func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "timeout") || strings.Contains(message, "tempor") || strings.Contains(message, "status 429") || strings.Contains(message, "status 5")
}

func buildOverpassQuery(query NearbyQuery) string {
	selector := overpassSelector(query.Type)
	return fmt.Sprintf(`[out:json][timeout:25];(
  node%s(around:%d,%f,%f);
  way%s(around:%d,%f,%f);
  relation%s(around:%d,%f,%f);
);out center tags;`, selector, query.Radius, query.Latitude, query.Longitude, selector, query.Radius, query.Latitude, query.Longitude, selector, query.Radius, query.Latitude, query.Longitude)
}

func overpassSelector(placeType string) string {
	switch placeType {
	case "hospital", "restaurant", "pharmacy":
		return fmt.Sprintf(`["amenity"="%s"]`, placeType)
	case "petrol pump":
		return `["amenity"="fuel"]`
	case "shop":
		return `["shop"]`
	default:
		return `["amenity"]`
	}
}

type overpassResponse struct {
	Elements []overpassElement `json:"elements"`
}

type overpassElement struct {
	Lat    float64           `json:"lat"`
	Lon    float64           `json:"lon"`
	Center *overpassCenter   `json:"center"`
	Tags   map[string]string `json:"tags"`
}

type overpassCenter struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

func (e overpassElement) coordinates() (float64, float64, bool) {
	if e.Lat != 0 || e.Lon != 0 {
		return e.Lat, e.Lon, true
	}
	if e.Center != nil {
		return e.Center.Lat, e.Center.Lon, true
	}
	return 0, 0, false
}

func (e overpassElement) displayName(placeType string) string {
	if value := strings.TrimSpace(e.Tags["name"]); value != "" {
		return value
	}
	if value := strings.TrimSpace(e.Tags["brand"]); value != "" {
		return value
	}
	if value := strings.TrimSpace(e.Tags["operator"]); value != "" {
		return value
	}
	if value := strings.TrimSpace(e.Tags["shop"]); value != "" {
		return value
	}
	return titleCase(placeType)
}

func (e overpassElement) addressString() string {
	parts := []string{
		e.Tags["addr:housenumber"],
		e.Tags["addr:street"],
		e.Tags["addr:suburb"],
		e.Tags["addr:city"],
		e.Tags["addr:state"],
		e.Tags["addr:postcode"],
		e.Tags["addr:country"],
	}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, ", ")
}

func titleCase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	words := strings.Fields(strings.ReplaceAll(value, "_", " "))
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	return strings.Join(words, " ")
}

func truncateLogBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 1000 {
		return text[:1000] + "..."
	}
	return text
}
