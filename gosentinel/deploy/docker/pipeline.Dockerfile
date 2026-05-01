FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /pipeline ./cmd/pipeline/

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /pipeline /pipeline
# Default alert rules — overridden by ConfigMap mount in Kubernetes
COPY config/alert-rules.yaml /config/alert-rules.yaml
ENTRYPOINT ["/pipeline"]
