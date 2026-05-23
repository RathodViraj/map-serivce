package mapservice

import (
	"context"
	"errors"
	"time"
)

const (
	DefaultListenAddr     = ":8080"
	DefaultOverpassURL    = "https://overpass-api.de/api/interpreter"
	DefaultRequestTimeout = 15 * time.Second
	DefaultHTTPTimeout    = 20 * time.Second
	DefaultCacheTTL       = 5 * time.Minute
	DefaultRetryCount     = 3
	DefaultRetryDelay     = 500 * time.Millisecond
	DefaultCORSOrigin     = "*"
	MinNearbyRadius       = 1
	MaxNearbyRadius       = 50000
	MinLatitude           = -90.0
	MaxLatitude           = 90.0
	MinLongitude          = -180.0
	MaxLongitude          = 180.0
)

var (
	ErrInvalidCoordinates = errors.New("invalid coordinates")
	ErrInvalidRadius      = errors.New("invalid radius")
	ErrInvalidPlaceType   = errors.New("invalid nearby place type")
)

type Config struct {
	ListenAddr       string
	OverpassURL      string
	RequestTimeout   time.Duration
	HTTPTimeout      time.Duration
	CacheTTL         time.Duration
	RetryCount       int
	RetryDelay       time.Duration
	RedisAddr        string
	RedisPassword    string
	RedisDB          int
	RedisDialTimeout time.Duration
	CORSOrigin       string
}

type NearbyQuery struct {
	Latitude  float64
	Longitude float64
	Type      string
	Radius    int
}

type NearbyPlace struct {
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Category  string  `json:"category"`
	Address   string  `json:"address"`
	Distance  float64 `json:"distance"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type APIResponse struct {
	Success bool      `json:"success"`
	Message string    `json:"message,omitempty"`
	Data    any       `json:"data,omitempty"`
	Error   *APIError `json:"error,omitempty"`
}

type Service interface {
	SearchNearby(ctx context.Context, query NearbyQuery) ([]NearbyPlace, error)
}

type Repository interface {
	SearchNearby(ctx context.Context, query NearbyQuery) ([]NearbyPlace, error)
}

type Cache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
}
