FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/bandwidth-quota ./cmd/bandwidth-quota

FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/bandwidth-quota /bandwidth-quota
EXPOSE 9300
USER nonroot
ENTRYPOINT ["/bandwidth-quota"]
CMD ["--listen=:9300", "--redis-addr=redis:6379"]
