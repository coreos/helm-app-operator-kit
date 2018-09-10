package main

import (
	"context"
	"os"
	"runtime"

	sdk "github.com/operator-framework/operator-sdk/pkg/sdk"
	k8sutil "github.com/operator-framework/operator-sdk/pkg/util/k8sutil"
	sdkVersion "github.com/operator-framework/operator-sdk/version"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/helm/pkg/kube"
	"k8s.io/helm/pkg/storage"
	"k8s.io/helm/pkg/storage/driver"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm"
	stub "github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/stub"
)

const (
	APIVersionEnvVar = "API_VERSION"
	KindEnvVar       = "KIND"
	HelmChartEnvVar  = "HELM_CHART"
)

func printVersion() {
	logrus.Infof("Go Version: %s", runtime.Version())
	logrus.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	logrus.Infof("operator-sdk Version: %v", sdkVersion.Version)
}

func main() {
	printVersion()

	resource := os.Getenv(APIVersionEnvVar)
	kind := os.Getenv(KindEnvVar)
	namespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		logrus.Fatalf("Failed to get watch namespace: %v", err)
	}
	if namespace == "*" {
		namespace = metav1.NamespaceAll
	}
	resyncPeriod := 5

	storageBackend := storage.Init(driver.NewMemory())
	tillerKubeClient := kube.New(nil)
	chartDir := os.Getenv(HelmChartEnvVar)

	controller := helm.NewInstaller(storageBackend, tillerKubeClient, chartDir)

	logrus.Infof("Watching %s, %s, %s, %d", resource, kind, namespace, resyncPeriod)

	sdk.Watch(resource, kind, namespace, resyncPeriod)
	sdk.Handle(stub.NewHandler(controller))
	sdk.Run(context.TODO())
}
