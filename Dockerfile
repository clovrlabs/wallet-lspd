FROM golang:1.19-alpine as builder

RUN apk add --update build-base

WORKDIR /build

ADD . . 

RUN go get .

RUN go build -o server . 


FROM alpine:3

COPY --from=builder  /build/server /server
ADD postgresql/migrations /migrations
RUN chmod +x /server

ENTRYPOINT [ "/server" ]