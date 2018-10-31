# Helm App Operator

![Travis CI Build Status](https://travis-ci.org/operator-framework/helm-app-operator-kit.svg?branch=master "Travis CI Build Status")

## Overview

The Helm App Operator is a component of the [Operator Framework](https://github.com/operator-framework), an open source toolkit to manage Kubernetes native applications, called Operators, in an effective, automated, and scalable way. Read more in the [introduction blog post](https://coreos.com/blog/introducing-operator-framework).

The Helm App Operator makes it possible to leverage a pre-existing Helm chart to deploy Kubernetes resources as a unified application. It was inspired by the [Lostromos project](https://github.com/wpengine/lostromos). The underlying operator was created using the [operator-sdk][operator_sdk].

## Quick Start

This quick start guide walks through the process of building the Helm App Operator and extending it with an example Helm Chart.

### Prerequisites

- [dep][dep_tool] version v0.5.0+.
- [go][go_tool] version v1.10+
- [docker][docker_tool] version 17.03+
- Access to a kubernetes v1.9.0+ cluster

### Install the Operator SDK CLI

First, checkout and install the operator-sdk CLI:

```bash
mkdir -p $GOPATH/src/github.com/operator-framework
cd $GOPATH/src/github.com/operator-framework
git clone https://github.com/operator-framework/operator-sdk.git
cd operator-sdk
make dep install
```

### Initial Setup

Checkout this Helm App Operator repository:

```bash
cd $GOPATH/src/github.com/operator-framework
git clone https://github.com/operator-framework/helm-app-operator-kit.git
cd helm-app-operator-kit/helm-app-operator
```

Vendor the dependencies

```bash
make dep
```

### Build the operator base image

Build the Helm App Operator base image and push it to a public registry, such as quay.io

```bash
export BASE_IMAGE=quay.io/example-inc/helm-app-operator:v0.0.1
operator-sdk build $BASE_IMAGE
docker push $BASE_IMAGE
```

## Build a customized operator

Once you have a base image, the next step is to customize it to watch your custom resources and automate the deployment of your Helm Charts.

### Configuration

The operator can be configured with a YAML file and environment variables.

#### YAML file

The `watches.yaml` file allows you to configure the operator to manage one or more custom resources and Helm charts. By default, the operator will look for this file at `/opt/helm/watches.yaml`. This value can be overridden by the `HELM_CHART_WATCHES` environment variable.

```yaml
- group: apache.org
  version: v1alpha1
  kind: Tomcat
  chart: /charts/tomcat
```

#### Environment variables

Name               | Description
-------------------|--------------------------------------------------------------------------------------
HELM_CHART_WATCHES | The path to a configuration file that defines the operator watches (default: `/opt/helm/watches.yaml`).
API_VERSION        | The `<group/version>` of the custom resource to watch.
KIND               | The `<Kind>` of the custom resource to watch.
HELM_CHART         | The path to a Helm chart directory
WATCH_NAMESPACE    | The namespace in which the operator should watch for custom resource changes.


**NOTE:** `API_VERSION`, `KIND`, and `HELM_CHART` are supported to maintain backwards compatibility with older versions of this operator. New projects should use the configuration file to configure the watched CRDs and Helm charts.

### Create a new project for your customized operator

We'll use a tomcat-operator as an example.

1. Create a project directory:

    ```bash
    mkdir -p tomcat-operator && cd tomcat-operator
    ```

2. Download the tomcat helm chart into `tomcat-operator/helm-charts/`:

    ```bash
    mkdir helm-charts
    wget -qO- https://storage.googleapis.com/kubernetes-charts/tomcat-0.1.0.tgz | tar vxz -C ./helm-charts
    ```

3. Create a watch configuration in `tomcat-operator/watches.yaml`:

    ```bash
    cat << EOF > watches.yaml
    ---
    - group: apache.org
      version: v1alpha1
      kind: Tomcat
      chart: /helm-charts/tomcat
    EOF
    ```

4. Create a Dockerfile:

    ```bash
    cat << EOF > Dockerfile
    FROM $BASE_IMAGE
    ADD watches.yaml /opt/helm/watches.yaml
    ADD helm-charts /helm-charts
    ENTRYPOINT ["/usr/local/bin/helm-app-operator"]
    EOF
    ```

5. Build the tomcat-operator Docker image

    ```bash
    export BASE_IMAGE=quay.io/example-inc/helm-app-operator:v0.0.1
    export TOMCAT_IMAGE=quay.io/example-inc/tomcat-operator:v0.0.1
    docker build -t $TOMCAT_IMAGE .
    docker push $TOMCAT_IMAGE
    ```

6. Create Kubernetes resource files for your CRD, CR, RBAC rules and operator

    The [examples](../examples/tomcat-operator) directory has examples of each of these files that can be used for the tomcat-operator (or modified for other uses).

    ```bash
    cp -r $GOPATH/src/github.com/operator-framework/helm-app-operator-kit/examples/tomcat-operator/ deploy/
    sed "s|REPLACE_IMAGE|$TOMCAT_IMAGE|" deploy/operator.yaml.template > deploy/operator.yaml && rm deploy/operator.yaml.template
    sed "s|REPLACE_IMAGE|$TOMCAT_IMAGE|" deploy/csv.yaml.template > deploy/csv.yaml && rm deploy/csv.yaml.template
    ```

7. Deploy the operator to your cluster

    ```bash
    export OPERATOR_NAMESPACE=default

    # As a simple deployment
    kubectl create -f deploy/crd.yaml
    kubectl create -n $OPERATOR_NAMESPACE -f deploy/rbac.yaml
    kubectl create -n $OPERATOR_NAMESPACE -f deploy/operator.yaml

    # OR

    # Using Operator Lifecycle Manager
    kubectl create -f deploy/crd.yaml
    kubectl create -n $OPERATOR_NAMESPACE -f deploy/csv.yaml
    ```

8. Create an instance of the Helm Chart

    ```bash
    kubectl create -n $OPERATOR_NAMESPACE -f deploy/cr.yaml
    ```

[dep_tool]:https://golang.github.io/dep/docs/installation.html
[go_tool]:https://golang.org/dl/
[docker_tool]:https://docs.docker.com/install/
[operator_sdk]:https://github.com/operator-framework/operator-sdk