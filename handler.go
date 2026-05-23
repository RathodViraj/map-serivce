package mapservice

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type NearbyHandler struct {
	service Service
}

func NewHandler(service Service) *NearbyHandler {
	return &NearbyHandler{service: service}
}

func (h *NearbyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{
			Success: false,
			Error:   &APIError{Code: "method_not_allowed", Message: "only GET is allowed"},
		})
		return
	}

	query, err := parseNearbyRequest(r)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{
			Success: false,
			Error:   &APIError{Code: "invalid_request", Message: err.Error()},
		})
		return
	}

	places, serviceErr := h.service.SearchNearby(r.Context(), query)
	if serviceErr != nil {
		status := http.StatusInternalServerError
		code := "internal_error"
		if isValidationError(serviceErr) {
			status = http.StatusBadRequest
			code = "invalid_request"
		}
		respondJSON(w, status, APIResponse{
			Success: false,
			Error:   &APIError{Code: code, Message: serviceErr.Error()},
		})
		return
	}

	respondJSON(w, http.StatusOK, APIResponse{
		Success: true,
		Message: "Nearby places fetched successfully",
		Data:    places,
	})
}

func parseNearbyRequest(r *http.Request) (NearbyQuery, error) {
	latitude, err := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("lat")), 64)
	if err != nil {
		return NearbyQuery{}, ErrInvalidCoordinates
	}
	longitude, err := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("lon")), 64)
	if err != nil {
		return NearbyQuery{}, ErrInvalidCoordinates
	}
	radius, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("radius")))
	if err != nil {
		return NearbyQuery{}, ErrInvalidRadius
	}
	placeType := strings.TrimSpace(r.URL.Query().Get("type"))
	if placeType == "" {
		return NearbyQuery{}, ErrInvalidPlaceType
	}

	return NearbyQuery{
		Latitude:  latitude,
		Longitude: longitude,
		Type:      placeType,
		Radius:    radius,
	}, nil
}

func respondJSON(w http.ResponseWriter, status int, payload APIResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(recorder, r)
		logJSON(map[string]any{
			"level":       "info",
			"method":      r.Method,
			"path":        r.URL.Path,
			"query":       r.URL.RawQuery,
			"status":      recorder.statusCode,
			"duration_ms": time.Since(start).Milliseconds(),
			"remote_addr": r.RemoteAddr,
		})
	})
}

func CORSMiddleware(allowedOrigin string) func(http.Handler) http.Handler {
	if strings.TrimSpace(allowedOrigin) == "" {
		allowedOrigin = DefaultCORSOrigin
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (s *statusRecorder) WriteHeader(statusCode int) {
	s.statusCode = statusCode
	s.ResponseWriter.WriteHeader(statusCode)
}

func logJSON(entry map[string]any) {
	payload, err := json.Marshal(entry)
	if err != nil {
		log.Printf("%v", entry)
		return
	}
	log.Printf("%s", payload)
}
