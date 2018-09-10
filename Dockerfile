FROM golang:1.10 as builder
ARG HELM_CHART
ARG API_VERSION
ARG KIND
WORKDIR /go/src/github.com/operator-framework/helm-app-operator-kit/helm-app-operator
COPY helm-app-operator .
# FIXME(alecmerdler): Takes forever to download all dependencies
# RUN curl https://raw.githubusercontent.com/golang/dep/master/install.sh | sh
# RUN dep ensure -v
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/operator cmd/helm-app-operator/main.go
RUN chmod +x bin/operator

FROM alpine:3.6
ARG HELM_CHART
ARG API_VERSION
ARG KIND
ENV API_VERSION $API_VERSION
ENV KIND $KIND
WORKDIR /
COPY --from=builder /go/src/github.com/operator-framework/helm-app-operator-kit/helm-app-operator/bin/operator /operator
ADD $HELM_CHART /chart
ENV HELM_CHART /chart

CMD ["/operator"]
