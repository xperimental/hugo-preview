FROM golang:1.22.5 AS builder

ENV DEBIAN_FRONTEND=noninteractive

WORKDIR /build

ENV LD_FLAGS="-w"
ENV CGO_ENABLED=0
RUN go get -u github.com/gobuffalo/packr/v2/packr2
RUN wget -O /tmp/hugo.tar.gz https://github.com/gohugoio/hugo/releases/download/v0.76.5/hugo_0.76.5_Linux-64bit.tar.gz \
 && tar xvzf /tmp/hugo.tar.gz -C /tmp

COPY go.mod go.sum /build/

RUN go mod download
RUN go mod verify

COPY . /build/

RUN packr2 install -v -tags netgo -ldflags "${LD_FLAGS}" .

FROM alpine

RUN apk add --no-cache git

LABEL maintainer="Robert Jacob <xperimental@solidproject.de>"
EXPOSE 8080
USER nobody
ENTRYPOINT ["/bin/hugo-preview"]

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /go/bin/hugo-preview /tmp/hugo /bin/
