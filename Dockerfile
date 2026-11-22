# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ravena ./cmd/ravena

FROM alpine:3.22
# ffmpeg: conversões; imagemagick: efeitos e figurinha animada → MP4 (o ffmpeg
# não lê WebP animado); yt-dlp: downloads. Todos rodam só sob demanda.
RUN apk add --no-cache ca-certificates tzdata ffmpeg imagemagick imagemagick-webp imagemagick-jpeg yt-dlp \
    && adduser -D -H -u 10001 ravena \
    && mkdir -p /data && chown ravena /data
COPY --from=build /out/ravena /usr/local/bin/ravena

USER ravena
ENV DATA_DIR=/data \
    TZ=America/Sao_Paulo \
    # Teto suave do heap do Go: o GC fica mais agressivo antes de chegar nele.
    GOMEMLIMIT=64MiB
ENTRYPOINT ["/usr/local/bin/ravena"]
