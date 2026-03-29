# Stage 1: Build the React frontend
FROM node:20-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

# Stage 2: Build the Go binary
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Overwrite frontend/dist with the freshly built assets so go:embed picks them up.
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -o /gleipnir .

# Stage 3: Minimal runtime image
FROM alpine:3.20
RUN apk --no-cache add ca-certificates
COPY --from=builder /gleipnir /usr/local/bin/gleipnir
RUN mkdir -p /data
EXPOSE 8080
CMD ["/usr/local/bin/gleipnir"]
