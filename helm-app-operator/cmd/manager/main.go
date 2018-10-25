// Copyright 2018 The Operator-SDK Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"
	"runtime"
	"time"

	k8sutil "github.com/operator-framework/operator-sdk/pkg/util/k8sutil"
	sdkVersion "github.com/operator-framework/operator-sdk/version"
	"github.com/sirupsen/logrus"
	"k8s.io/helm/pkg/storage"
	"k8s.io/helm/pkg/storage/driver"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/client"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/controller"
)

func printVersion() {
	logrus.Infof("Go Version: %s", runtime.Version())
	logrus.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	logrus.Infof("operator-sdk Version: %v", sdkVersion.Version)
}

func main() {
	printVersion()
	flag.Parse()

	namespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		logrus.Fatalf("failed to get watch namespace: %v", err)
	}

	// TODO: Expose metrics port after SDK uses controller-runtime's dynamic client
	// sdk.ExposeMetricsPort()

	cfg, err := config.GetConfig()
	if err != nil {
		logrus.Fatal(err)
	}

	mgr, err := manager.New(cfg, manager.Options{Namespace: namespace})
	if err != nil {
		logrus.Fatal(err)
	}

	logrus.Print("Registering Components.")

	// Create Tiller's storage backend and kubernetes client
	storageBackend := storage.Init(driver.NewMemory())
	tillerKubeClient, err := client.NewFromManager(mgr)
	if err != nil {
		logrus.Fatal(err)
	}

	installers, err := helm.NewInstallersFromEnv(storageBackend, tillerKubeClient)
	if err != nil {
		logrus.Fatal(err)
	}

	for gvk, installer := range installers {
		// Register the controller with the manager.
		controller.Add(mgr, controller.WatchOptions{
			Namespace:    namespace,
			GVK:          gvk,
			Installer:    installer,
			ResyncPeriod: 5 * time.Second,
		})
	}

	logrus.Print("Starting the Cmd.")

	// Start the Cmd
	logrus.Fatal(mgr.Start(signals.SetupSignalHandler()))
}
