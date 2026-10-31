# Single image: Go API + built web UI + bundled demo site and example profiles.
FROM node:22-alpine AS web
WORKDIR /src/apps/web
COPY apps/web/package.json apps/web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY apps/web ./
RUN npm run build

FROM golang:1.24-alpine AS api
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/api ./apps/api && CGO_ENABLED=0 go build -o /out/scraper ./apps/cli

FROM alpine:3.20
WORKDIR /app
COPY --from=api /out/api /out/scraper /usr/local/bin/
COPY --from=web /src/apps/web/dist ./web
COPY examples ./examples
ENV PDS_ADDR=:8080 PDS_PROFILES=/app/examples/sources PDS_WEB=/app/web
EXPOSE 8080
ENTRYPOINT ["api"]
