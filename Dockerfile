FROM golang:bullseye AS build
WORKDIR /build
ADD go.mod .
ADD go.sum .
RUN go mod download
ADD . .
RUN ls -lah
RUN go build -o server .

FROM golang:bullseye
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /build/server /usr/bin/program
ENTRYPOINT ["/usr/bin/program"]