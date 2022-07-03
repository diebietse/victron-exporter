FROM golang:1.18.3-alpine3.16 as builder

RUN mkdir /workdir
WORKDIR /workdir

COPY . .

RUN go build .

FROM alpine:3.16
ENTRYPOINT ["/usr/local/bin/victron-exporter"]
COPY --from=builder /workdir/victron-exporter /usr/local/bin
