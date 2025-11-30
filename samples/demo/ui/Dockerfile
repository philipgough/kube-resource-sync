# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY main.go ./

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o file-watcher .

# Final stage
FROM alpine:latest

# Install ca-certificates for any HTTPS requests
RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy the binary from builder stage
COPY --from=builder /app/file-watcher .

# Create the watched directory
RUN mkdir -p /watched

# Expose port
EXPOSE 8080

# Run the binary
CMD ["./file-watcher"]