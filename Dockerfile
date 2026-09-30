FROM golang:1.27-alpine AS build
WORKDIR /src
COPY bot/go.mod bot/go.sum ./
RUN go mod download
COPY bot/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/skinbot .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
 && adduser -S -u 10001 bot \
 && mkdir /data && chown bot /data
WORKDIR /app
COPY --from=build /out/skinbot ./
ENV DB_PATH=/data/bot.db \
    CREDIT="tg: @faustyu"
USER bot
VOLUME /data
CMD ["/app/skinbot"]
