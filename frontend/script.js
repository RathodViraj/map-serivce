const state = {
  map: null,
  userMarker: null,
  placeLayer: null,
  placeMarkers: new Map(),
  results: [],
  location: null,
  inFlightSignature: "",
  lastCompletedSignature: "",
  currentAbortController: null,
  searchTimer: null,
  renderToken: 0,
  activePlaceKey: "",
  isLoading: false,
};

const categoryLabels = {
  hospital: "Hospital",
  restaurant: "Restaurant",
  pharmacy: "Pharmacy",
  fuel: "Fuel",
  shop: "Shop",
};

const categoryColors = {
  hospital: "hospital",
  restaurant: "restaurant",
  pharmacy: "pharmacy",
  fuel: "fuel",
  shop: "shop",
};

document.addEventListener("DOMContentLoaded", () => {
  logLocationEvent("page_loaded", {
    href: window.location.href,
    protocol: window.location.protocol,
    host: window.location.host,
    secureContext: window.isSecureContext,
    geolocationSupported: Boolean(navigator.geolocation),
  });
  maybeRedirectToLocalhost().then((redirected) => {
    if (redirected) {
      return;
    }

    bindElements();
    initializeMap();
    bindEvents();
    requestCurrentLocation();
  });
});

const elements = {};

function bindElements() {
  elements.categorySelect = document.getElementById("categorySelect");
  elements.radiusRange = document.getElementById("radiusRange");
  elements.radiusValue = document.getElementById("radiusValue");
  elements.resultsList = document.getElementById("resultsList");
  elements.resultCount = document.getElementById("resultCount");
  elements.statusText = document.getElementById("statusText");
  elements.statusBadge = document.getElementById("statusBadge");
  elements.latitudeText = document.getElementById("latitudeText");
  elements.longitudeText = document.getElementById("longitudeText");
  elements.loadingOverlay = document.getElementById("loadingOverlay");
  elements.loadingTitle = document.getElementById("loadingTitle");
  elements.loadingSubtitle = document.getElementById("loadingSubtitle");
  elements.refreshLocationButton = document.getElementById("refreshLocationButton");
}

function initializeMap() {
  state.map = L.map("map", { zoomControl: true, preferCanvas: true }).setView([20, 0], 2);

  L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
    maxZoom: 19,
  }).addTo(state.map);

  state.placeLayer = L.layerGroup().addTo(state.map);
}

function bindEvents() {
  elements.categorySelect.addEventListener("change", () => scheduleSearch());
  elements.radiusRange.addEventListener("input", () => {
    updateRadiusLabel();
    scheduleSearch();
  });
  elements.refreshLocationButton.addEventListener("click", () => requestCurrentLocation(true));
}

function requestCurrentLocation(force = false) {
  logLocationEvent("location_request_started", {
    force,
    href: window.location.href,
    secureContext: window.isSecureContext,
    geolocationSupported: Boolean(navigator.geolocation),
  });

  logGeolocationPermissionState();

  if (!navigator.geolocation) {
    logLocationEvent("location_request_failed", {
      reason: "geolocation_not_supported",
    }, "error");
    setStatus("Geolocation is not supported in this browser.", "error");
    return;
  }

  if (!isGeolocationAvailable()) {
    logLocationEvent("location_request_failed", {
      reason: "insecure_origin",
      href: window.location.href,
      secureContext: window.isSecureContext,
      host: window.location.host,
    }, "error");
    setLoading(false);
    setStatus(
      `Location access requires a secure origin. Current URL: ${window.location.href}. Open the app from http://localhost:8080 instead of file:// or a remote IP.`,
      "error"
    );
    return;
  }

  if (!force && state.location) {
    scheduleSearch(true);
    return;
  }

  setLoading(true, "Requesting location", "Allow location access to search nearby services.");
  setStatus("Waiting for browser location permission...", "loading");

  requestGeolocation({
    enableHighAccuracy: true,
    timeout: 10000,
    maximumAge: 0,
  })
    .then((position) => {
      handleLocationSuccess(position);
    })
    .catch((error) => {
      if (error.code === error.TIMEOUT) {
        logLocationEvent("location_request_retrying", {
          reason: "timeout",
          message: error.message,
        }, "warn");

        setStatus("Location lookup timed out. Retrying with a broader fallback...", "loading");

        return requestGeolocation({
          enableHighAccuracy: false,
          timeout: 25000,
          maximumAge: 60000,
        }).then((retryPosition) => {
          logLocationEvent("location_request_retry_success", {
            latitude: retryPosition.coords.latitude,
            longitude: retryPosition.coords.longitude,
            accuracy: retryPosition.coords.accuracy,
          }, "warn");
          handleLocationSuccess(retryPosition);
        });
      }

      throw error;
    })
    .catch((error) => {
      handleLocationError(error);
    });
}

async function maybeRedirectToLocalhost() {
  if (window.location.protocol !== "file:") {
    return false;
  }

  const localCandidates = ["http://localhost:8080", "http://127.0.0.1:8080"];
  for (const candidate of localCandidates) {
    try {
      const controller = new AbortController();
      const timeoutId = window.setTimeout(() => controller.abort(), 1200);
      const response = await fetch(`${candidate}/health`, {
        signal: controller.signal,
        cache: "no-store",
      });
      window.clearTimeout(timeoutId);

      if (response.ok) {
        logLocationEvent("redirecting_to_localhost", {
          from: window.location.href,
          to: `${candidate}/`,
        }, "warn");
        window.location.replace(`${candidate}/`);
        return true;
      }
    } catch (error) {
      logLocationEvent("localhost_probe_failed", {
        candidate,
        message: error.message,
      }, "warn");
    }
  }

  return false;
}

function isGeolocationAvailable() {
  if (window.isSecureContext) {
    return true;
  }

  const hostname = window.location.hostname;
  return hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1";
}

function requestGeolocation(options) {
  logLocationEvent("location_request_options", options);

  return new Promise((resolve, reject) => {
    navigator.geolocation.getCurrentPosition(resolve, reject, options);
  });
}

function handleLocationSuccess(position) {
  logLocationEvent("location_request_success", {
    latitude: position.coords.latitude,
    longitude: position.coords.longitude,
    accuracy: position.coords.accuracy,
  });

  state.location = {
    lat: position.coords.latitude,
    lon: position.coords.longitude,
  };
  updateLocationView();
  moveUserMarker();
  state.map.setView([state.location.lat, state.location.lon], 15, { animate: true });
  setStatus("Location captured. Searching nearby places...", "loading");
  scheduleSearch(true);
}

function handleLocationError(error) {
  logLocationEvent(
    "location_request_error",
    {
      code: error.code,
      message: error.message,
      permissionDenied: error.code === error.PERMISSION_DENIED,
      positionUnavailable: error.code === error.POSITION_UNAVAILABLE,
      timeout: error.code === error.TIMEOUT,
      href: window.location.href,
      secureContext: window.isSecureContext,
    },
    "error"
  );

  let message = "Unable to access your location.";
  if (error.code === error.PERMISSION_DENIED) {
    message = "Location permission was denied. Enable it to search nearby services.";
  } else if (error.code === error.POSITION_UNAVAILABLE) {
    message = "Current location is unavailable right now.";
  } else if (error.code === error.TIMEOUT) {
    message = "Location lookup timed out. The browser could not resolve your position.";
  } else if (error.message) {
    message = error.message;
  }

  setLoading(false);
  setStatus(message, "error");
}

function logGeolocationPermissionState() {
  if (!navigator.permissions || !navigator.permissions.query) {
    logLocationEvent("geolocation_permission_state_unavailable", {
      reason: "permissions_api_not_supported",
    }, "warn");
    return;
  }

  navigator.permissions
    .query({ name: "geolocation" })
    .then((result) => {
      logLocationEvent("geolocation_permission_state", {
        state: result.state,
      });

      result.onchange = () => {
        logLocationEvent("geolocation_permission_state_changed", {
          state: result.state,
        }, "warn");
      };
    })
    .catch((error) => {
      logLocationEvent("geolocation_permission_state_error", {
        message: error.message,
      }, "warn");
    });
}

function logLocationEvent(eventName, details = {}, level = "info") {
  const payload = {
    event: eventName,
    timestamp: new Date().toISOString(),
    ...details,
  };

  if (level === "error") {
    console.error("[location]", payload);
    return;
  }

  if (level === "warn") {
    console.warn("[location]", payload);
    return;
  }

  console.info("[location]", payload);
}

function updateRadiusLabel() {
  const value = Number(elements.radiusRange.value || 0);
  elements.radiusValue.textContent = value >= 1000 ? `${(value / 1000).toFixed(value % 1000 === 0 ? 0 : 1)} km` : `${value} m`;
}

function scheduleSearch(immediate = false) {
  if (!state.location) {
    return;
  }

  clearTimeout(state.searchTimer);
  if (immediate) {
    void performSearch();
    return;
  }
  state.searchTimer = window.setTimeout(() => {
    void performSearch();
  }, 350);
}

async function performSearch() {
  if (!state.location) {
    return;
  }

  const category = elements.categorySelect.value;
  const radius = Number(elements.radiusRange.value);
  const signature = requestSignature(category, radius, state.location);

  if (signature === state.lastCompletedSignature || signature === state.inFlightSignature) {
    return;
  }

  if (state.currentAbortController) {
    state.currentAbortController.abort();
  }

  state.currentAbortController = new AbortController();
  state.inFlightSignature = signature;
  setLoading(true, "Searching nearby places", `${categoryLabels[category]} within ${formatRadius(radius)}.`);
  setStatus(`Searching for nearby ${categoryLabels[category].toLowerCase()}s...`, "loading");

  const url = buildApiUrl(category, radius, state.location);

  try {
    const response = await fetch(url, { signal: state.currentAbortController.signal });
    if (!response.ok) {
      throw new Error(`Request failed with status ${response.status}`);
    }

    const payload = await response.json();
    if (!payload || !payload.success) {
      const message = payload?.error?.message || "Unexpected API response";
      throw new Error(message);
    }

    const places = Array.isArray(payload.data) ? payload.data : [];
    state.results = places.map((place) => normalizePlace(place));
    state.lastCompletedSignature = signature;
    renderResults(state.results);
    syncMarkers(state.results);
    setResultCount(state.results.length);

    if (state.results.length === 0) {
      setStatus(`No ${categoryLabels[category].toLowerCase()}s found within ${formatRadius(radius)}.`, "idle");
    } else {
      setStatus(`Found ${state.results.length} nearby places.`, "success");
    }
  } catch (error) {
    if (error.name === "AbortError") {
      return;
    }
    state.results = [];
    clearPlaces();
    renderResults([]);
    setResultCount(0);
    setStatus(error.message || "Search failed.", "error");
  } finally {
    setLoading(false);
    state.inFlightSignature = "";
  }
}

function buildApiUrl(category, radius, location) {
  const url = new URL("/nearby", window.location.origin);
  url.searchParams.set("lat", location.lat.toFixed(6));
  url.searchParams.set("lon", location.lon.toFixed(6));
  url.searchParams.set("type", category);
  url.searchParams.set("radius", String(radius));
  return url.toString();
}

function requestSignature(category, radius, location) {
  return [category, radius, location.lat.toFixed(5), location.lon.toFixed(5)].join("|");
}

function normalizePlace(place) {
  const latitude = Number(place.latitude);
  const longitude = Number(place.longitude);
  const distance = Number(place.distance || 0);
  const category = String(place.category || "").toLowerCase();
  return {
    name: place.name || categoryLabels[category] || "Unnamed place",
    latitude,
    longitude,
    category,
    address: place.address || "Address unavailable",
    distance,
  };
}

function syncMarkers(places) {
  const nextKeys = new Set();

  for (const place of places) {
    const key = getPlaceKey(place);
    nextKeys.add(key);
    let marker = state.placeMarkers.get(key);
    const icon = createPlaceIcon(place.category);

    if (!marker) {
      marker = L.marker([place.latitude, place.longitude], { icon });
      marker.addTo(state.placeLayer);
      marker.on("click", () => focusPlace(key));
      state.placeMarkers.set(key, marker);
    } else {
      marker.setLatLng([place.latitude, place.longitude]);
      marker.setIcon(icon);
    }

    marker.bindPopup(createPopupHtml(place), { closeButton: true, maxWidth: 240 });
  }

  for (const [key, marker] of state.placeMarkers.entries()) {
    if (!nextKeys.has(key)) {
      state.placeLayer.removeLayer(marker);
      state.placeMarkers.delete(key);
    }
  }
}

function clearPlaces() {
  state.placeLayer.clearLayers();
  state.placeMarkers.clear();
  state.activePlaceKey = "";
}

function renderResults(places) {
  const currentToken = ++state.renderToken;
  elements.resultsList.innerHTML = "";

  if (places.length === 0) {
    const emptyState = document.createElement("div");
    emptyState.className = "result-item";
    emptyState.innerHTML = `
      <div class="result-item__top">
        <div>
          <p class="result-item__name">No places found</p>
          <div class="result-item__meta">Try a larger radius or another category.</div>
        </div>
      </div>
    `;
    elements.resultsList.appendChild(emptyState);
    return;
  }

  const chunkSize = 12;
  let index = 0;

  const renderChunk = () => {
    if (currentToken !== state.renderToken) {
      return;
    }

    const fragment = document.createDocumentFragment();
    const end = Math.min(index + chunkSize, places.length);

    for (; index < end; index += 1) {
      const place = places[index];
      const key = getPlaceKey(place);
      const button = document.createElement("button");
      button.type = "button";
      button.className = "result-item";
      button.dataset.placeKey = key;
      button.innerHTML = `
        <div class="result-item__top">
          <div>
            <p class="result-item__name">${escapeHtml(place.name)}</p>
            <div class="result-item__meta">
              <span>${escapeHtml(categoryLabels[place.category] || place.category)}</span>
              <span>${formatDistance(place.distance)}</span>
            </div>
          </div>
          <span class="result-distance">${formatDistance(place.distance)}</span>
        </div>
        <div class="result-item__address">${escapeHtml(place.address)}</div>
      `;
      button.addEventListener("click", () => focusPlace(key));
      fragment.appendChild(button);
    }

    elements.resultsList.appendChild(fragment);

    if (index < places.length) {
      requestAnimationFrame(renderChunk);
    }
  };

  requestAnimationFrame(renderChunk);
}

function focusPlace(placeKey) {
  const place = state.results.find((item) => getPlaceKey(item) === placeKey);
  const marker = state.placeMarkers.get(placeKey);
  if (!place || !marker) {
    return;
  }

  state.activePlaceKey = placeKey;
  state.map.flyTo([place.latitude, place.longitude], 17, { animate: true, duration: 0.6 });
  marker.openPopup();
  highlightActiveResult(placeKey);
}

function highlightActiveResult(placeKey) {
  document.querySelectorAll(".result-item.is-active").forEach((item) => item.classList.remove("is-active"));
  const active = elements.resultsList.querySelector(`[data-place-key="${CSS.escape(placeKey)}"]`);
  if (active) {
    active.classList.add("is-active");
    active.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }
}

function moveUserMarker() {
  if (!state.location) {
    return;
  }

  const icon = createUserIcon();
  if (!state.userMarker) {
    state.userMarker = L.marker([state.location.lat, state.location.lon], { icon }).addTo(state.map);
    state.userMarker.bindPopup("You are here");
  } else {
    state.userMarker.setLatLng([state.location.lat, state.location.lon]);
    state.userMarker.setIcon(icon);
  }
}

function updateLocationView() {
  if (!state.location) {
    elements.latitudeText.textContent = "--";
    elements.longitudeText.textContent = "--";
    return;
  }
  elements.latitudeText.textContent = state.location.lat.toFixed(6);
  elements.longitudeText.textContent = state.location.lon.toFixed(6);
}

function setLoading(isLoading, title = "Searching nearby places", subtitle = "Please wait while we update the map.") {
  state.isLoading = isLoading;
  elements.loadingTitle.textContent = title;
  elements.loadingSubtitle.textContent = subtitle;
  elements.loadingOverlay.classList.toggle("hidden", !isLoading);
}

function setStatus(message, kind) {
  elements.statusText.textContent = message;
  elements.statusBadge.className = `status-badge status-badge--${kind}`;
  elements.statusBadge.textContent = kind.charAt(0).toUpperCase() + kind.slice(1);
}

function setResultCount(count) {
  elements.resultCount.textContent = String(count);
}

function formatDistance(distance) {
  if (!Number.isFinite(distance) || distance < 0) {
    return "--";
  }
  if (distance < 1000) {
    return `${Math.round(distance)} m`;
  }
  return `${(distance / 1000).toFixed(distance >= 10000 ? 0 : 1)} km`;
}

function formatRadius(radius) {
  if (radius >= 1000) {
    return `${(radius / 1000).toFixed(radius % 1000 === 0 ? 0 : 1)} km`;
  }
  return `${radius} m`;
}

function getPlaceKey(place) {
  return [place.category, place.name, place.latitude.toFixed(6), place.longitude.toFixed(6)].join("|");
}

function createPlaceIcon(category) {
  const resolvedCategory = categoryColors[category] || "shop";
  return L.divIcon({
    className: "",
    html: `<div class="place-pin place-pin--${resolvedCategory}"></div>`,
    iconSize: [28, 28],
    iconAnchor: [14, 14],
    popupAnchor: [0, -12],
  });
}

function createUserIcon() {
  return L.divIcon({
    className: "",
    html: `<div class="user-pin"></div>`,
    iconSize: [30, 30],
    iconAnchor: [15, 15],
    popupAnchor: [0, -12],
  });
}

function createPopupHtml(place) {
  return `
    <div class="place-popup">
      <h3>${escapeHtml(place.name)}</h3>
      <p><strong>Category:</strong> ${escapeHtml(categoryLabels[place.category] || place.category)}</p>
      <p><strong>Distance:</strong> ${escapeHtml(formatDistance(place.distance))}</p>
      <p><strong>Address:</strong> ${escapeHtml(place.address)}</p>
    </div>
  `;
}

function escapeHtml(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}
