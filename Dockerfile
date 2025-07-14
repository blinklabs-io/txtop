FROM ghcr.io/blinklabs-io/go:1.24.5-1 AS build

WORKDIR /code
COPY . .
RUN make build

FROM cgr.dev/chainguard/glibc-dynamic AS txtop
COPY --from=build /code/txtop /bin/
ENTRYPOINT ["txtop"]
