package helm

import (
	"fmt"
	"strings"

	"bytes"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/helm/pkg/chartutil"
	"k8s.io/helm/pkg/proto/hapi/chart"
	"k8s.io/helm/pkg/releaseutil"
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
// Adds the ownerrefs to all the documents in a YAML file
func (o *OwnerRefEngine) addOwnerRefs(fileContents string) (string, error) {
	const documentSeparator = "---\n"
	var outBuf bytes.Buffer

	for _, manifest := range releaseutil.SplitManifests(fileContents) {
		manifestMap := chartutil.FromYaml(manifest)
		if errors, ok := manifestMap["Error"]; ok {
			return "", fmt.Errorf("error parsing rendered template to add ownerrefs: %v", errors)
		}

		// Check if the document is empty
		if len(manifestMap) == 0 {
			continue
		}

		unst, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&manifestMap)
		if err != nil {
			return "", err
		}

		unstructured := &unstructured.Unstructured{Object: unst}
		unstructured.SetOwnerReferences(o.refs)

		// Write the document with owner ref to the buffer
		_, err = outBuf.WriteString(chartutil.ToYaml(unstructured.Object) + documentSeparator)
		if err != nil {
			return "", fmt.Errorf("error writing the document to buffer : %v", err)
		}
	}

	return outBuf.String(), nil
}

// NewOwnerRefEngine creates a new OwnerRef engine with a set of metav1.OwnerReferences to be added to assets
func NewOwnerRefEngine(baseEngine environment.Engine, refs []metav1.OwnerReference) environment.Engine {
	return &OwnerRefEngine{
		Engine: baseEngine,
		refs:   refs,
	}
}
