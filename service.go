package mapservice

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	nearbyCacheVersion    = "v2"
	nearbyRefreshLockTTL  = 30 * time.Second
	maxCachedPayloadBytes = 64 * 1024
)

var nearbyRadiusBands = []int{250, 500, 1000, 2500, 5000, 10000, 25000, 50000}

type NearbyService struct {
	repository     Repository
	cache          Cache
	cacheTTL       time.Duration
	requestTimeout time.Duration
	refreshes      sync.Map
}

type cacheLocker interface {
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
}

type nearbyCacheEntry struct {
	Version             int           `json:"v"`
	CachedAtUnixMilli   int64         `json:"c"`
	FreshUntilUnixMilli int64         `json:"f"`
	StaleUntilUnixMilli int64         `json:"s"`
	RadiusBand          int           `json:"r"`
	Places              []NearbyPlace `json:"p"`
}

func NewService(repository Repository, cache Cache, cacheTTL, requestTimeout time.Duration) *NearbyService {
	if cacheTTL <= 0 {
		cacheTTL = DefaultCacheTTL
	}
	if requestTimeout <= 0 {
		requestTimeout = DefaultRequestTimeout
	}
	return &NearbyService{
		repository:     repository,
		cache:          cache,
		cacheTTL:       cacheTTL,
		requestTimeout: requestTimeout,
	}
}

func (s *NearbyService) SearchNearby(ctx context.Context, query NearbyQuery) ([]NearbyPlace, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalizedType, err := normalizePlaceType(query.Type)
	if err != nil {
		return nil, err
	}
	if err := validateNearbyQuery(query.Latitude, query.Longitude, query.Radius); err != nil {
		return nil, err
	}

	query.Type = normalizedType
	cacheRadius := cacheRadiusBucket(query.Radius)
	cacheQuery := query
	cacheQuery.Radius = cacheRadius
	cacheKey := buildCacheKey(cacheQuery)
	now := time.Now().UTC()
	if s.cache != nil {
		if cached, cacheErr := s.cache.Get(ctx, cacheKey); cacheErr == nil && cached != "" {
			if entry, ok := decodeCacheEntry(cached); ok {
				if entry.isFresh(now) || entry.isStale(now) {
					places := filterPlacesWithinRadius(entry.Places, query.Radius)
					if entry.isStale(now) {
						s.refreshCache(cacheKey, query, cacheRadius)
					}
					return places, nil
				}
			}
		}
	}

	requestCtx := ctx
	var cancel context.CancelFunc
	if s.requestTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, s.requestTimeout)
		defer cancel()
	}

	places, err := s.repository.SearchNearby(requestCtx, cacheQuery)
	if err != nil {
		return nil, err
	}

	for index := range places {
		places[index].Distance = haversineMeters(query.Latitude, query.Longitude, places[index].Latitude, places[index].Longitude)
	}

	sort.SliceStable(places, func(i, j int) bool {
		if places[i].Distance == places[j].Distance {
			return strings.ToLower(places[i].Name) < strings.ToLower(places[j].Name)
		}
		return places[i].Distance < places[j].Distance
	})

	responsePlaces := filterPlacesWithinRadius(places, query.Radius)

	if s.cache != nil {
		freshTTL := cacheFreshTTL(cacheRadius, s.cacheTTL)
		staleTTL := cacheStaleTTL(cacheRadius, s.cacheTTL)
		if payload, marshalErr := marshalCacheEntry(nearbyCacheEntry{
			Version:             2,
			CachedAtUnixMilli:   now.UnixMilli(),
			FreshUntilUnixMilli: now.Add(freshTTL).UnixMilli(),
			StaleUntilUnixMilli: now.Add(staleTTL).UnixMilli(),
			RadiusBand:          cacheRadius,
			Places:              places,
		}); marshalErr == nil && len(payload) <= maxCachedPayloadBytes {
			_ = s.cache.Set(requestCtx, cacheKey, payload, staleTTL)
		}
	}

	return responsePlaces, nil
}

func validateNearbyQuery(latitude, longitude float64, radius int) error {
	if latitude < MinLatitude || latitude > MaxLatitude || longitude < MinLongitude || longitude > MaxLongitude {
		return ErrInvalidCoordinates
	}
	if radius < MinNearbyRadius || radius > MaxNearbyRadius {
		return ErrInvalidRadius
	}
	return nil
}

func normalizePlaceType(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "_", " ")
	normalized = strings.ReplaceAll(normalized, "-", " ")
	normalized = strings.Join(strings.Fields(normalized), " ")

	switch normalized {
	case "hospital", "restaurant", "pharmacy", "shop":
		return normalized, nil
	case "petrol", "petrol pump", "fuel", "gas station":
		return "petrol pump", nil
	default:
		return "", ErrInvalidPlaceType
	}
}

func buildCacheKey(query NearbyQuery) string {
	precision := cacheCoordinatePrecision(query.Radius)
	return fmt.Sprintf("nearby:%s:%s:%s", nearbyCacheVersion, query.Type, geoTileHash(query.Latitude, query.Longitude, precision))
}

func cacheRadiusBucket(radius int) int {
	for _, band := range nearbyRadiusBands {
		if radius <= band {
			return band
		}
	}
	return MaxNearbyRadius
}

func cacheCoordinatePrecision(radius int) int {
	switch {
	case radius <= 250:
		return 5
	case radius <= 1000:
		return 4
	case radius <= 5000:
		return 3
	case radius <= 25000:
		return 2
	default:
		return 1
	}
}

func geoTileHash(latitude, longitude float64, precision int) string {
	factor := math.Pow10(precision)
	latBucket := int64(math.Round((latitude + 90.0) * factor))
	lonBucket := int64(math.Round((longitude + 180.0) * factor))
	return strconv.FormatInt(latBucket, 36) + ":" + strconv.FormatInt(lonBucket, 36)
}

func cacheFreshTTL(radius int, baseTTL time.Duration) time.Duration {
	var ttl time.Duration
	switch {
	case radius <= 250:
		ttl = 45 * time.Second
	case radius <= 1000:
		ttl = 2 * time.Minute
	case radius <= 5000:
		ttl = 5 * time.Minute
	case radius <= 25000:
		ttl = 10 * time.Minute
	default:
		ttl = 15 * time.Minute
	}
	if baseTTL > 0 && ttl > baseTTL {
		ttl = baseTTL
	}
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return ttl
}

func cacheStaleTTL(radius int, baseTTL time.Duration) time.Duration {
	fresh := cacheFreshTTL(radius, baseTTL)
	stale := fresh * 4
	if stale < fresh+2*time.Minute {
		stale = fresh + 2*time.Minute
	}
	if stale > 30*time.Minute {
		stale = 30 * time.Minute
	}
	return stale
}

func filterPlacesWithinRadius(places []NearbyPlace, radius int) []NearbyPlace {
	if len(places) == 0 {
		return nil
	}
	filtered := places[:0]
	for _, place := range places {
		if place.Distance <= float64(radius) {
			filtered = append(filtered, place)
		}
	}
	return filtered
}

func decodeCacheEntry(value string) (nearbyCacheEntry, bool) {
	var entry nearbyCacheEntry
	if err := json.Unmarshal([]byte(value), &entry); err != nil {
		return nearbyCacheEntry{}, false
	}
	if entry.Version != 2 {
		return nearbyCacheEntry{}, false
	}
	return entry, true
}

func marshalCacheEntry(entry nearbyCacheEntry) (string, error) {
	payload, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (e nearbyCacheEntry) isFresh(now time.Time) bool {
	return now.UnixMilli() <= e.FreshUntilUnixMilli
}

func (e nearbyCacheEntry) isStale(now time.Time) bool {
	return now.UnixMilli() <= e.StaleUntilUnixMilli
}

func (s *NearbyService) refreshCache(cacheKey string, query NearbyQuery, cacheRadius int) {
	if s.cache == nil || s.repository == nil {
		return
	}
	refreshKey := cacheKey + ":refresh"
	if _, loaded := s.refreshes.LoadOrStore(refreshKey, struct{}{}); loaded {
		return
	}
	go func() {
		defer s.refreshes.Delete(refreshKey)

		refreshCtx := context.Background()
		var cancel context.CancelFunc
		if s.requestTimeout > 0 {
			refreshCtx, cancel = context.WithTimeout(refreshCtx, s.requestTimeout)
			defer cancel()
		}

		if locker, ok := s.cache.(cacheLocker); ok {
			locked, lockErr := locker.SetNX(refreshCtx, refreshKey, strconv.FormatInt(time.Now().UnixNano(), 10), nearbyRefreshLockTTL)
			if lockErr != nil || !locked {
				if lockErr != nil {
					log.Printf(`{"level":"warn","message":"background refresh lock failed","key":%q,"error":%q}`, cacheKey, lockErr.Error())
				}
				return
			}
		}

		refreshQuery := query
		refreshQuery.Radius = cacheRadius
		places, err := s.repository.SearchNearby(refreshCtx, refreshQuery)
		if err != nil {
			log.Printf(`{"level":"warn","message":"background refresh failed","key":%q,"error":%q}`, cacheKey, err.Error())
			return
		}

		for index := range places {
			places[index].Distance = haversineMeters(query.Latitude, query.Longitude, places[index].Latitude, places[index].Longitude)
		}

		sort.SliceStable(places, func(i, j int) bool {
			if places[i].Distance == places[j].Distance {
				return strings.ToLower(places[i].Name) < strings.ToLower(places[j].Name)
			}
			return places[i].Distance < places[j].Distance
		})

		now := time.Now().UTC()
		freshTTL := cacheFreshTTL(cacheRadius, s.cacheTTL)
		staleTTL := cacheStaleTTL(cacheRadius, s.cacheTTL)
		payload, marshalErr := marshalCacheEntry(nearbyCacheEntry{
			Version:             2,
			CachedAtUnixMilli:   now.UnixMilli(),
			FreshUntilUnixMilli: now.Add(freshTTL).UnixMilli(),
			StaleUntilUnixMilli: now.Add(staleTTL).UnixMilli(),
			RadiusBand:          cacheRadius,
			Places:              places,
		})
		if marshalErr != nil || len(payload) > maxCachedPayloadBytes {
			return
		}
		_ = s.cache.Set(refreshCtx, cacheKey, payload, staleTTL)
	}()
}

func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadius = 6371000.0
	lat1Rad := degreesToRadians(lat1)
	lon1Rad := degreesToRadians(lon1)
	lat2Rad := degreesToRadians(lat2)
	lon2Rad := degreesToRadians(lon2)

	dLat := lat2Rad - lat1Rad
	dLon := lon2Rad - lon1Rad

	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadius * c
}

func degreesToRadians(value float64) float64 {
	return value * math.Pi / 180.0
}

func isValidationError(err error) bool {
	return err == ErrInvalidCoordinates || err == ErrInvalidRadius || err == ErrInvalidPlaceType
}
