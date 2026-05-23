FROM golang:1.25-alpine AS builder

WORKDIR /src

RUN apk add --no-cache ca-certificates

COPY . .

ENV CGO_ENABLED=0
ENV GOOS=linux

RUN go build -trimpath -ldflags="-s -w -buildid=" -o /out/app ./cmd/main.go

FROM alpine:3.20

RUN apk add --no-cache ca-certificates && adduser -D -g '' appuser

WORKDIR /app

ENV PORT=8080

COPY --from=builder /out/app /app/app
COPY --from=builder /src/frontend /app/frontend

RUN chown -R appuser:appuser /app

USER appuser

EXPOSE 8080

CMD ["/app/app"]
