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

package helm

import (
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/helm/pkg/chartutil"
	"k8s.io/helm/pkg/proto/hapi/chart"
	"k8s.io/helm/pkg/tiller/environment"
)

// OwnerRefEngine wraps a tiller Render engine, adding ownerrefs to rendered assets
type OwnerRefEngine struct {
	environment.Engine
	refs []metav1.OwnerReference
}

// assert interface
var _ environment.Engine = &OwnerRefEngine{}

// Render proxies to the wrapped Render engine and then adds ownerRefs to each rendered file
func (o *OwnerRefEngine) Render(chart *chart.Chart, values chartutil.Values) (map[string]string, error) {
	rendered, err := o.Engine.Render(chart, values)
	if err != nil {
		return nil, err
	}

	ownedRenderedFiles := map[string]string{}
	for fileName, renderedFile := range rendered {
		if !strings.HasSuffix(fileName, ".yaml") {
			continue
		}
		logrus.Debugf("adding ownerrefs to file: %s", fileName)
		withOwner, err := o.addOwnerRefs(renderedFile)
		if err != nil {
			return nil, err
		}
		if withOwner == "" {
			logrus.Debugf("skipping empty template: %s", fileName)
			continue
		}
		ownedRenderedFiles[fileName] = withOwner
	}
	return ownedRenderedFiles, nil
}

// addOwnerRefs adds the configured ownerRefs to a single rendered file
func (o *OwnerRefEngine) addOwnerRefs(fileContents string) (string, error) {
	parsed := chartutil.FromYaml(fileContents)
	if errors, ok := parsed["Error"]; ok {
		return "", fmt.Errorf("error parsing rendered template to add ownerrefs: %v", errors)
	}

	// Empty input
	if len(parsed) == 0 {
		return "", nil
	}

	unst, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&parsed)
	if err != nil {
		return "", err
	}
	unstructured := &unstructured.Unstructured{Object: unst}
	unstructured.SetOwnerReferences(o.refs)
	return chartutil.ToYaml(unstructured.Object), nil
}

// NewOwnerRefEngine creates a new OwnerRef engine with a set of metav1.OwnerReferences to be added to assets
func NewOwnerRefEngine(baseEngine environment.Engine, refs []metav1.OwnerReference) environment.Engine {
	return &OwnerRefEngine{
		Engine: baseEngine,
		refs:   refs,
	}
}
