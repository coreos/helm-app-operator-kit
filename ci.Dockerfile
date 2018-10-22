FROM golang:1.10 as builder
ENV API_VERSION 'test.com/v1'
WORKDIR /go/src/github.com/operator-framework/helm-app-operator-kit/helm-app-operator
COPY helm-app-operator .
RUN ./gofmt.sh
RUN curl https://raw.githubusercontent.com/golang/dep/master/install.sh | sh
RUN dep ensure -v
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/operator cmd/manager/main.go
RUN chmod +x bin/operator
