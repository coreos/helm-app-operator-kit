# [DEPRECATED] Helm App Operator Kit

**This project is deprecated. Its functionality has been integrated into the [Operator SDK][operator-sdk] as of v0.3.0. To get started developing a Helm operator with the SDK, see the [Helm operator user guide][helm-user-guide].**

## Overview

This project is a component of the [Operator Framework](https://github.com/operator-framework), an open source toolkit to manage Kubernetes native applications, called Operators, in an effective, automated, and scalable way. Read more in the [introduction blog post](https://coreos.com/blog/introducing-operator-framework).

This repository serves as a template for easily creating managed stateless applications that run Helm charts. It was inspired by the [Lostromos project](https://github.com/wpengine/lostromos). The underlying Operator was created using the `operator-sdk new` command.

While the [Operator Lifecycle Manager][olm-repo] can only manage Operators, not all applications require developers to write a custom Operator.
The [Helm App Operator Kit][helm-sdk] makes it possible to leverage a pre-existing Helm chart to deploy Kubernetes resources as a unified application.

## Clone the project

```sh
mkdir -p $GOPATH/src/github.com/operator-framework
cd $GOPATH/src/github.com/operator-framework
git clone https://github.com/operator-framework/helm-app-operator-kit
cd helm-app-operator-kit
```

## Getting Started

See the [helm-app-operator](./helm-app-operator) subdirectory for more details about how to build and deploy a custom operator with Helm App Operator Kit or follow along with a simple [tomcat-operator](./examples/tomcat-operator) example.

[operator-sdk]: https://github.com/operator-framework/operator-sdk
[helm-user-guide]:https://github.com/operator-framework/operator-sdk/blob/master/doc/helm/user-guide.md
[helm-sdk]: ./
[olm-repo]: https://github.com/operator-framework/operator-lifecycle-manager
