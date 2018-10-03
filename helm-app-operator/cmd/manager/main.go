package main

import (
	"flag"
	"log"
	"runtime"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/controller"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/installer"

	k8sutil "github.com/operator-framework/operator-sdk/pkg/util/k8sutil"
	sdkVersion "github.com/operator-framework/operator-sdk/version"

	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/helm/pkg/storage"
	"k8s.io/helm/pkg/storage/driver"

	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
)

func printVersion() {
	log.Printf("Go Version: %s", runtime.Version())
	log.Printf("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	log.Printf("operator-sdk Version: %v", sdkVersion.Version)
}

func main() {
	printVersion()
	flag.Parse()

	namespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		log.Fatalf("Failed to get watch namespace: %v", err)
	}

	// TODO: Expose metrics port after SDK uses controller-runtime's dynamic client
	// sdk.ExposeMetricsPort()

	// Get a config to talk to the apiserver
	cfg, err := config.GetConfig()
	if err != nil {
		log.Fatal(err)
	}

	// Create a new Cmd to provide shared dependencies and start components
	mgr, err := manager.New(cfg, manager.Options{Namespace: namespace})
	if err != nil {
		log.Fatal(err)
	}

	log.Print("Registering Components.")

	// Create Tiller's kubernetes client and storage backend to be shared
	// across all helm installers.
	tillerKubeClient := installer.NewTillerClientFromManager(mgr)
	storageBackend := storage.Init(driver.NewMemory())

	// Dynamically load the CR watchers and helm installers based on the
	// environment.
	watches, err := installer.NewFromEnv(tillerKubeClient, storageBackend)
	if err != nil {
		log.Fatal(err)
	}

	// Register all of the watches with the manager.
	done := signals.SetupSignalHandler()
	for gvk, i := range watches {
		controller.Add(mgr, controller.WatchOptions{
			GVK:         gvk,
			Namespace:   namespace,
			Installer:   i,
			StopChannel: done,
		})
	}

	log.Print("Starting the Cmd.")

	// Start the Cmd
	log.Fatal(mgr.Start(done))
}
