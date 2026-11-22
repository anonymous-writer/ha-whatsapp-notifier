# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS builder
RUN apk add --no-cache build-base
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN go mod tidy && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/whatsapp-bridge .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata libgcc
WORKDIR /app
COPY --from=builder /out/whatsapp-bridge /usr/local/bin/whatsapp-bridge

ENV PORT=8099
EXPOSE 8099
VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/whatsapp-bridge"]
