FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/codebase-intel-server ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/codebase-intel-server /usr/local/bin/codebase-intel-server

EXPOSE 8090
CMD ["codebase-intel-server", "-transport", "http", "-addr", "0.0.0.0:8090"]
