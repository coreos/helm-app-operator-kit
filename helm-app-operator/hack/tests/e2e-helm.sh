#!/usr/bin/env bash

#===================================================================
# FUNCTION trap_add ()
#
# Purpose:  prepends a command to a trap
#
# - 1st arg:  code to add
# - remaining args:  names of traps to modify
#
# Example:  trap_add 'echo "in trap DEBUG"' DEBUG
#
# See: http://stackoverflow.com/questions/3338030/multiple-bash-traps-for-the-same-signal
#===================================================================
trap_add() {
    trap_add_cmd=$1; shift || fatal "${FUNCNAME} usage error"
    new_cmd=
    for trap_add_name in "$@"; do
        # Grab the currently defined trap commands for this trap
        existing_cmd=`trap -p "${trap_add_name}" |  awk -F"'" '{print $2}'`

        # Define default command
        [ -z "${existing_cmd}" ] && existing_cmd="echo exiting @ `date`"

        # Generate the new command
        new_cmd="${trap_add_cmd};${existing_cmd}"

        # Assign the test
         trap   "${new_cmd}" "${trap_add_name}" || \
                fatal "unable to add to trap ${trap_add_name}"
    done
}

set -ex

TAG=$(git rev-parse --short HEAD)
BASE_IMAGE=quay.io/example/helm-app-operator:${TAG}
MEMCACHED_IMAGE=quay.io/example/memcached-operator:${TAG}

# switch to the "default" namespace if on openshift, to match the minikube test
if which oc 2>/dev/null; then oc project default; fi

# build operator base image
docker build -t ${BASE_IMAGE} -f build/Dockerfile .

# build a memcached operator
pushd test
pushd memcached-operator
DIR1=$(pwd)

mkdir chart && wget -qO- https://storage.googleapis.com/kubernetes-charts/memcached-2.3.1.tgz | tar -xzv --strip-components=1 -C ./chart
trap_add 'rm -rf ${DIR1}/chart' EXIT

docker build --build-arg BASE_IMAGE=${BASE_IMAGE} -t ${MEMCACHED_IMAGE} .
sed "s|REPLACE_IMAGE|${MEMCACHED_IMAGE}|g" deploy/operator.yaml.tmpl > deploy/operator.yaml
sed -i "s|Always|Never|g" deploy/operator.yaml
trap_add 'rm ${DIR1}/deploy/operator.yaml' EXIT

# deploy the operator
kubectl create -f deploy/rbac.yaml
trap_add 'kubectl delete -f ${DIR1}/deploy/rbac.yaml' EXIT

kubectl create -f deploy/crd.yaml
trap_add 'kubectl delete -f ${DIR1}/deploy/crd.yaml' EXIT

kubectl create -f deploy/operator.yaml
trap_add 'kubectl delete -f ${DIR1}/deploy/operator.yaml' EXIT


# wait for operator pod to run
if ! timeout 1m kubectl rollout status deployment/memcached-operator;
then
    kubectl describe deployment memcached-operator
    kubectl logs deployment/memcached-operator
    exit 1
fi

# create CR
kubectl create -f deploy/cr.yaml
trap_add 'kubectl delete --ignore-not-found -f ${DIR1}/deploy/cr.yaml' EXIT
if ! timeout 20s bash -c -- 'until kubectl get memcacheds.helm.example.com my-test-app -o jsonpath="{..status.release.info.status.code}" | grep 1; do sleep 1; done';
then
    kubectl logs deployment/memcached-operator
fi

release_name=$(kubectl get memcacheds.helm.example.com my-test-app -o jsonpath="{..status.release.name}")
memcached_statefulset=$(kubectl get statefulset -l release=${release_name} -o jsonpath="{..metadata.name}")
kubectl patch statefulset ${memcached_statefulset} -p '{"spec":{"updateStrategy":{"type":"RollingUpdate"}}}'
if ! timeout 1m kubectl rollout status statefulset/${memcached_statefulset};
then
    kubectl describe pods -l release=${release_name}
    kubectl describe statefulsets ${memcached_statefulset}
    kubectl logs statefulset/${memcached_statefulset}
    exit 1
fi

kubectl delete -f deploy/cr.yaml --wait=true
kubectl logs deployment/memcached-operator | grep "Uninstalled release for apiVersion=helm.example.com/v1alpha1 kind=Memcached name=default/my-test-app"

popd
popd
