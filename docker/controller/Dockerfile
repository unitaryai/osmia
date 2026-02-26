# syntax=docker/dockerfile:1

# Stage 1: Build the controller binary.
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/robodev ./cmd/robodev/

# Stage 2: Minimal runtime image.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/robodev /usr/local/bin/robodev

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["robodev"]
