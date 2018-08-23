This project is a component of the [Operator Framework](https://github.com/operator-framework), an open source toolkit to manage Kubernetes native applications, called Operators, in an effective, automated, and scalable way. Read more in the [introduction blog post](https://coreos.com/blog/introducing-operator-framework).

# Helm App Operator Kit

[![Docker Repository on Quay](https://quay.io/repository/operatorframework/helm-app-operator-ci/status "Docker Repository on Quay")](https://quay.io/repository/operatorframework/helm-app-operator-ci)

This repository serves as a template for easily creating managed stateless applications that run either Kubernetes Deployments or Helm charts. It was inspired by the [Lostromos project](https://github.com/wpengine/lostromos). The underlying Operator was created using the `operator-sdk new` command.

## Installing a custom Helm-based app

While the [Operator Lifecycle Manager][olm-repo] can only manage Operators, not all applications require developers to write a custom Operator.
The [Helm App Operator Kit][helm-sdk] makes it possible to leverage a pre-existing Helm chart to deploy Kubernetes resources as a unified application.

```sh
git clone https://github.com/coreos/helm-app-operator-kit
cd helm-app-operator-kit
```

asdfsadf

### Prerequisites

- Kubernetes 1.9+ cluster
- `docker` client
- `kubectl` client
- Helm Chart

### Instructions

1) Run the following:

```sh
$ git checkout git@github.com:operator-framework/helm-app-operator-kit.git && cd helm-app-operator-kit
$ cd helm-app-operator && dep ensure && cd ..
$ docker build -t quay.io/<namespace>/<chart>-operator --build-arg HELM_CHART=/path/to/helm/chart --build-arg API_VERSION=<group/version> --build-arg KIND=<Kind> .
$ docker push quay.io/<namespace>/<chart>-operator
```

2) Modify the following Kubernetes YAML manifest files in `helm-app-operator/deploy`:

File                          | Action
------------------------------|--------------------------------------------------------------------------------------------------------
`deploy/crd.yaml`             | Define your CRD (`kind`, `spec.version`, `spec.group` *must* match the `docker build` args)
`deploy/cr.yaml`              | Make an instance of your custom resource (`kind`, `apiVersion` *must match the `docker build` args)
`deploy/operator.yaml`        | Replace `<namespace>` and `<chart>` appropriately
`deploy/rbac.yaml`            | Ensure the resources created by your chart are properly listed
`deploy/olm-catalog/csv.yaml`             | Replace fields appropriately. Define RBAC in `spec.install.spec.permissions`. Ensure `spec.customresourcedefinitions.owned` correctly contains your CRD
`deploy/olm-catalog/crd.yaml` | Define your CRD (`kind`, `spec.version`, `spec.group` *must* match the `docker build` args)

3) Apply the manifests to your Kubernetes cluster (if using Operator Lifecycle Manager):

```sh
$ kubectl create -f helm-app-operator/deploy/olm-catalog/crd.yaml
$ kubectl create -n <operator-namespace> -f helm-app-operator/deploy/olm-catalog/csv.yaml
```

Otherwise, manually create the RBAC and deployment resources:

```sh
$ kubectl create -f helm-app-operator/deploy/crd.yaml
$ kubectl create -n <operator-namespace> -f helm-app-operator/deploy/rbac.yaml
$ kubectl create -n <operator-namespace> -f helm-app-operator/deploy/operator.yaml
```

### Creating an instance of the example application

After the `CustomResourceDefinition` and `ClusterServiceVersion-v1` resources for the new application have been applied, new instances of that app can be created:

```sh
$ kubectl create -n <operator-namespace> -f helm-app-operator/deploy/cr.yaml
```

Confirm the resources defined in the Helm Chart were created.

[helm-sdk]: https://github.com/coreos/helm-app-operator-kit
[olm-repo]: https://github.com/operator-framework/operator-lifecycle-manager
