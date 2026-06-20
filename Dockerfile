FROM node:22-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.22-alpine AS backend
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum* ./
RUN go mod download
COPY backend ./backend
COPY --from=frontend /src/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -o /out/sub2api-origin-lg ./backend/cmd/server

FROM alpine:3.20
WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=backend /out/sub2api-origin-lg /app/sub2api-origin-lg
COPY --from=frontend /src/frontend/dist /app/frontend/dist
COPY config.example.yaml /app/config.example.yaml
EXPOSE 8080
ENTRYPOINT ["/app/sub2api-origin-lg"]
