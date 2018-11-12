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

package release

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/helm/pkg/kube"
	"k8s.io/helm/pkg/storage"
)

// ManagerFactory can create new Helm release Managers given a custom resource.
type ManagerFactory interface {
	NewManager(*unstructured.Unstructured) Manager
}

type managerFactory struct {
	storageBackend   *storage.Storage
	tillerKubeClient *kube.Client
	chartDir         string
}

func (f *managerFactory) NewManager(u *unstructured.Unstructured) Manager {
	return newManagerForCR(f.storageBackend, f.tillerKubeClient, f.chartDir, u)
}
