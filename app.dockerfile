FROM golang:1.25-alpine AS builder

WORKDIR /src

RUN apk add --no-cache ca-certificates tzdata

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64

ENV CGO_ENABLED=0
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH

RUN go build -trimpath -ldflags="-s -w -buildid=" -o /out/app ./cmd/main.go

RUN cat <<'EOF' >/tmp/healthcheck.go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	url := "http://127.0.0.1:" + port + "/health"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		fmt.Fprintln(os.Stderr, resp.Status)
		os.Exit(1)
	}
}
EOF

RUN go build -trimpath -ldflags="-s -w -buildid=" -o /out/healthcheck /tmp/healthcheck.go

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

EXPOSE 8080

ENV PORT=8080
ENV GIN_MODE=release

COPY --from=builder /out/app /app/app
COPY --from=builder /out/healthcheck /app/healthcheck
COPY --from=builder /src/frontend /app/frontend

USER nonroot:nonroot

ENTRYPOINT ["/app/app"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=20s --retries=3 CMD ["/app/healthcheck"]
