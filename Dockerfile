# ---- build ----
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go index.html ./
# index.html is embedded into the binary via go:embed, so the final image
# needs nothing but the binary itself.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/app .

# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /bin/app /app
EXPOSE 8080
ENV PORT=8080
USER nonroot:nonroot
ENTRYPOINT ["/app"]
