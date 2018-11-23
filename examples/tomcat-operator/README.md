# Example: Tomcat Operator

Simple Operator using the official [Tomcat Helm Chart](https://github.com/kubernetes/charts/tree/master/stable/tomcat) and deployed directly or using the [Operator Lifecycle Manager](https://github.com/operator-framework/operator-lifecycle-manager).

## Build and push the tomcat-operator container

```sh
export IMAGE=quay.io/<namespace>/tomcat-operator:v0.0.1
docker build \
  --build-arg HELM_CHART=https://storage.googleapis.com/kubernetes-charts/tomcat-0.1.0.tgz \
  --build-arg API_VERSION=apache.org/v1alpha1 \
  --build-arg KIND=Tomcat \
  -t $IMAGE ../../

docker push $IMAGE
```

## Deploying the tomcat-operator to your cluster

### As a deployment:

```sh
kubectl create -f crd.yaml
kubectl create -n <operator-namespace> -f rbac.yaml

sed "s|REPLACE_IMAGE|$IMAGE|" operator.yaml.template > operator.yaml
kubectl create -n <operator-namespace> -f operator.yaml
```

### Using the Operator Lifecycle Manager:

NOTE: Operator Lifecycle Manager must be [installed](https://github.com/operator-framework/operator-lifecycle-manager/blob/master/Documentation/install/install.md) in the cluster in advance.

```sh
kubectl create -f crd.yaml

sed "s|REPLACE_IMAGE|$IMAGE|" csv.yaml.template > csv.yaml
kubectl create -n <operator-namespace> -f csv.yaml
```

## Deploying an instance of tomcat

```sh
kubectl create -n <operator-namespace> -f cr.yaml
```
