FROM golang:1.23-alpine AS builder
LABEL org.opencontainers.image.source=https://github.com/shogomuranushi/infrabox
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o infrabox-api .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/infrabox-api /usr/local/bin/
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["infrabox-api"]
