set -ev

TAG=$(git rev-parse --short HEAD)

# switch to the "default" namespace if on openshift, to match the minikube test
if which oc 2>/dev/null; then oc project default; fi

# build operator binary and base image
pushd helm-app-operator
dep ensure
./build/build.sh
go test ./...
docker build -t quay.io/example/helm-app-operator:${TAG} -f build/Dockerfile .
popd

# build a memcached operator
pushd test
pushd memcached-operator
docker build --build-arg TAG=${TAG} -t quay.io/example/memcached-operator:${TAG} .

sed "s|REPLACE_IMAGE|quay.io/example/memcached-operator:${TAG}|g" deploy/operator.yaml.tmpl > deploy/operator.yaml
sed -i "s|Always|Never|g" deploy/operator.yaml

# deploy the operator
kubectl create -f deploy/rbac.yaml
kubectl create -f deploy/crd.yaml
kubectl create -f deploy/operator.yaml

# wait for operator pod to run
if ! timeout 1m kubectl rollout status deployment/memcached-operator;
then
    kubectl describe deployment memcached-operator
    kubectl logs deployment/memcached-operator
    exit 1
fi

# create CR
kubectl create -f deploy/cr.yaml
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

# Test finalizer
kubectl delete -f deploy/cr.yaml --wait=true
kubectl logs deployment/memcached-operator | grep "Uninstalled release for apiVersion=helm.example.com/v1alpha1 kind=Memcached name=default/my-test-app"

# Cleanup resources
kubectl delete -f deploy/operator.yaml
kubectl delete -f deploy/crd.yaml
kubectl delete -f deploy/rbac.yaml
rm deploy/operator.yaml

popd
popd
