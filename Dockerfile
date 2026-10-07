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

# Корневой и промежуточные сертификаты Минцифры (НУЦ). Без них TLS
# до portal.fedsfm.ru не проверяется: x509: certificate signed by unknown authority.
COPY certs/russian_trusted_root_ca.crt certs/russian_trusted_sub_ca.crt certs/russian_trusted_sub_ca_2024.crt /usr/local/share/ca-certificates/
RUN update-ca-certificates

COPY --from=build /out/rosfin-terrorists /usr/local/bin/rosfin-terrorists

USER rosfin
WORKDIR /data
VOLUME ["/data"]

ENV ROSFIN_OUTPUT_DIR=/data \
    TZ=Europe/Moscow

ENTRYPOINT ["/usr/local/bin/rosfin-terrorists"]
