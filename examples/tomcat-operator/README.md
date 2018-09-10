# Example: Tomcat Operator

Simple Operator using the official [Tomcat Helm Chart](https://github.com/kubernetes/charts/tree/master/stable/tomcat) and deployed using the [Operator Lifecycle Manager](https://github.com/operator-framework/operator-lifecycle-manager).

Run the following from this directory:
```sh
$ mkdir chart && wget -qO- https://storage.googleapis.com/kubernetes-charts/tomcat-0.1.0.tgz | tar -xzv --strip-components=1 -C ./chart
$ docker build \
  --build-arg HELM_CHART=./examples/tomcat-operator/chart \
  --build-arg API_VERSION=apache.org/v1alpha1 \
  --build-arg KIND=Tomcat \
  -t quay.io/<namespace>/tomcat-operator ../../
$ docker push quay.io/<namespace>/tomcat-operator
$ kubectl create -f crd.yaml
$ kubectl create -n <operator-namespace> -f csv.yaml
```
