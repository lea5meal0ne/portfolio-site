FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /portfolio-site .
FROM alpine:3.22
RUN addgroup -g 10001 portfolio && adduser -D -u 10001 -G portfolio portfolio && mkdir /data && chown portfolio:portfolio /data
COPY --from=builder /portfolio-site /usr/local/bin/portfolio-site
USER portfolio
ENV ADDR=0.0.0.0:8080 DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["portfolio-site"]
