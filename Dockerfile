FROM golang:1.27-alpine AS build
WORKDIR /src
COPY bot/go.mod bot/go.sum ./
RUN go mod download
COPY bot/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/skinbot .

FROM python:3.13-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends fonts-dejavu-core \
 && rm -rf /var/lib/apt/lists/* \
 && pip install --no-cache-dir pillow \
 && useradd --system --uid 10001 bot \
 && mkdir /data && chown bot /data
WORKDIR /app
COPY skin_papercraft.py bbmodel_papercraft.py ./
COPY --from=build /out/skinbot ./
ENV DB_PATH=/data/bot.db \
    GENERATOR=/app/skin_papercraft.py \
    MODEL_GENERATOR=/app/bbmodel_papercraft.py \
    PYTHON=python3
USER bot
VOLUME /data
CMD ["/app/skinbot"]
