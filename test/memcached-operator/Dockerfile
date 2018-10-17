ARG BASE_IMAGE
FROM $BASE_IMAGE

ENV HELM_CHART_WATCHES /watches.yaml
ADD ./watches.yaml /watches.yaml
ADD ./chart /chart

CMD ["/usr/local/bin/helm-app-operator"]
