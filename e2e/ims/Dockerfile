FROM golang:1.26 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /ims ./cmd/ims

FROM debian:trixie-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends iproute2 tcpdump \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /ims /usr/local/bin/ims
