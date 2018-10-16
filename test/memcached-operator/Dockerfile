ARG TAG
FROM quay.io/example/helm-app-operator:$TAG

ENV HELM_CHART_WATCHES /watches.yaml
ADD ./watches.yaml /watches.yaml
ADD ./chart /chart

CMD ["/usr/local/bin/helm-app-operator"]
