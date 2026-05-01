FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Templates and static files are embedded via go:embed at compile time
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /ui ./cmd/ui/

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /ui /ui
ENTRYPOINT ["/ui"]
