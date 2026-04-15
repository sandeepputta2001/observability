FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /order-service ./examples/services/order-service/

FROM gcr.io/distroless/static-debian12
COPY --from=builder /order-service /order-service
ENTRYPOINT ["/order-service"]
