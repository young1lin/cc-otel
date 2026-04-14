FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /cc-otel ./cmd/cc-otel/

FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY --from=builder /cc-otel /usr/local/bin/cc-otel
EXPOSE 4317 8899
VOLUME /data
CMD ["cc-otel", "serve", "-config", "/data/cc-otel.yaml"]
