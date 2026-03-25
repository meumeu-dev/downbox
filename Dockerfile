FROM golang:1-alpine AS builder

WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=docker" -o /downbox .

FROM alpine:3.20

RUN apk add --no-cache aria2 ca-certificates

COPY --from=builder /downbox /usr/local/bin/downbox

RUN adduser -D -h /home/downbox downbox
USER downbox

ENV DOWNBOX_BIND=0.0.0.0

VOLUME /downloads
EXPOSE 8080
EXPOSE 6881-6999

HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://localhost:8080/api/login || exit 1

ENTRYPOINT ["downbox"]
CMD ["-download-dir", "/downloads"]
