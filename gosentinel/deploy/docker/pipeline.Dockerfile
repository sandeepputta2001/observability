FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /pipeline ./cmd/pipeline/

FROM gcr.io/distroless/static-debian12
COPY --from=builder /pipeline /pipeline
COPY config/alert-rules.yaml /config/alert-rules.yaml
ENTRYPOINT ["/pipeline"]
