# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go_src/go.mod ./
RUN go mod download
COPY go_src/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/rosfin-terrorists .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 rosfin \
    && mkdir -p /data \
    && chown rosfin:rosfin /data

COPY --from=build /out/rosfin-terrorists /usr/local/bin/rosfin-terrorists

USER rosfin
WORKDIR /data
VOLUME ["/data"]

ENV ROSFIN_OUTPUT_DIR=/data \
    ROSFIN_INTERVAL=12h \
    ROSFIN_FORMATS=xml,doc \
    TZ=Europe/Moscow

ENTRYPOINT ["/usr/local/bin/rosfin-terrorists"]
