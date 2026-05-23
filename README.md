# Map Service [render](https://nearby-finder.onrender.com/)

A Go-based nearby search service that queries OpenStreetMap via the Overpass API, enriches and sorts results, and serves them through a small HTTP API and a static Leaflet frontend.

## Architecture Overview

The application is organized as a single Go module named `mapservice`.

- `cmd/main.go` bootstraps the HTTP server, loads environment variables, wires dependencies, and serves both the API and the static frontend.
- `handler.go` parses requests, validates input, and returns JSON responses.
- `service.go` contains the core nearby-search workflow, including normalization, caching, sorting, filtering, and background cache refresh behavior.
- `repository.go` is responsible for calling the Overpass API and turning raw OpenStreetMap responses into domain objects.
- `db/redis.go` provides a lightweight Redis client used for caching.
- `frontend/` contains the static Leaflet UI that calls `GET /nearby`.

### Current Flow

```
Browser / Leaflet Frontend
	   |
	   v
   HTTP Server in cmd/main.go
	   |
	   +--> GET /health
	   |
	   +--> GET /nearby
		     |
		     v
	     handler.go
		     |
		     v
	     service.go
	   /       |      \
	  /        |       \
	 v         v        v
   Cache  Validation  Overpass API
	 |                     |
	 +---------<-----------+
		     |
		     v
	   Sorted Nearby Places JSON
```

## Runtime Flow

1. The browser sends a request to `GET /nearby?lat=...&lon=...&radius=...&type=...`.
2. The handler validates and normalizes the query.
3. The service builds a cache key from a versioned geo-tile hash, the normalized place type, and a radius band.
4. If Redis has a fresh or stale entry, the service can return cached data immediately.
5. When cached data is stale, the service serves the stale result and triggers a background refresh.
6. If the cache is missing, the service queries the Overpass API, computes distances, sorts results, and stores the response back in Redis.
7. The frontend is served from the same backend process, so deployment only needs one application container plus Redis.

## Cache Strategy

The current caching approach is designed for nearby search workloads where small coordinate changes should not create completely separate cache entries.

- Cache keys are versioned so the format can evolve safely.
- Coordinates are bucketed into geo tiles instead of using raw latitude and longitude values.
- Radius values are grouped into bands to reduce key explosion.
- Cache entries store metadata for fresh and stale windows.
- Stale entries can be served while a background refresh updates Redis.
- Large payloads are skipped to avoid wasting memory.

This gives better hit rates than exact-match keys while keeping the cache bounded and predictable.

## Key Components

### HTTP Layer

The HTTP server exposes:

- `GET /nearby` for search requests
- `GET /health` for liveness checks
- `/` for the static frontend

CORS and request logging are handled centrally in middleware.

### Service Layer

`NearbyService` is the main orchestration layer. It handles:

- place-type normalization
- coordinate and radius validation
- cache lookup and cache write-through
- stale-cache reads with background refresh
- distance calculation and final sorting
- radius-based response filtering

### Repository Layer

`OverpassRepository`:

- builds the Overpass query
- retries transient failures
- parses API responses into `NearbyPlace` values
- removes duplicate places

### Redis Layer

Redis is optional at runtime. If Redis is unavailable, the app still serves requests without caching.

The Redis client currently supports:

- `GET`
- `SET`
- `SET NX` for refresh locking

That lock prevents multiple instances from refreshing the same stale cache entry at the same time.

## Data Model

A nearby search request uses:

- `lat` for latitude
- `lon` for longitude
- `radius` in meters
- `type` for the place category

Returned places include:

- name
- latitude
- longitude
- category
- address
- distance

## Deployment

The repository includes a production Docker setup:

- `app.dockerfile` uses a multi-stage build.
- The final image is a small Alpine runtime.
- The static frontend is copied into the runtime image.
- `docker-compose.yml` runs the backend and Redis on a dedicated bridge network.
- Healthchecks and restart policies are configured for production-style deployment.

This setup is suitable for Docker-based deployment platforms, including Render.

## Configuration

The application reads configuration from environment variables. Common settings include:

- `PORT`
- `OVERPASS_URL`
- `REQUEST_TIMEOUT`
- `HTTP_TIMEOUT`
- `CACHE_TTL`
- `RETRY_COUNT`
- `RETRY_DELAY`
- `REDIS_ADDR`
- `REDIS_PASSWORD`
- `REDIS_DB`
- `REDIS_DIAL_TIMEOUT`
- `CORS_ORIGIN`

`cmd/main.go` also loads `.env` from the workspace root or parent directory for local development.

`REDIS_ADDR` can be a plain host like `localhost:6379` or a full Redis URL.

## Development Notes

- The backend serves the frontend from the container filesystem, so the browser app and API stay in sync.
- Redis is used as a performance optimization, not as a hard dependency.
- The cache key design favors locality over exact coordinate matching, which is more suitable for nearby search traffic.

## Health Endpoint

The service exposes `GET /health` and the Docker image includes a small built-in healthcheck binary that probes that endpoint.

## Repository Layout

- `app.dockerfile` - production Docker build
- `docker-compose.yml` - local production-like stack
- `cmd/` - application entrypoint
- `db/` - Redis client
- `frontend/` - static frontend
- `handler.go` - HTTP request handling
- `model.go` - shared types and constants
- `repository.go` - Overpass API integration
- `service.go` - nearby search orchestration and caching
