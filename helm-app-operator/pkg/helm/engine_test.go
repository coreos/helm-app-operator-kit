package helm

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/helm/pkg/chartutil"
	"k8s.io/helm/pkg/proto/hapi/chart"
)

type mockEngine struct {
	out map[string]string
}

func (e *mockEngine) Render(chrt *chart.Chart, v chartutil.Values) (map[string]string, error) {
	return e.out, nil
}

func TestOwnerRefEngine(t *testing.T) {
	ownerRefs := []metav1.OwnerReference{
		{
			APIVersion: "v1",
			Kind:       "Test",
			Name:       "test",
			UID:        "123",
		},
	}

	baseOut := `apiVersion: stable.nicolerenee.io/v1
kind: Character
metadata:
  name: nemo
spec:
  Name: Nemo
`

	expectedOut := `apiVersion: stable.nicolerenee.io/v1
kind: Character
metadata:
  name: nemo
  ownerReferences:
  - apiVersion: v1
    kind: Test
    name: test
    uid: "123"
spec:
  Name: Nemo
---
`
	expected := map[string]string{"template.yaml": expectedOut, "template2.yaml": expectedOut}

	baseEngineOutput := map[string]string{
		"template.yaml":  baseOut,
		"template2.yaml": baseOut,
		"empty.yaml":     "",
		"comment.yaml":   "# This is empty",
	}

	engine := NewOwnerRefEngine(&mockEngine{out: baseEngineOutput}, ownerRefs)
	out, err := engine.Render(&chart.Chart{}, map[string]interface{}{})
	require.NoError(t, err)
	require.EqualValues(t, expected, out)
}


func TestMultiDocumentFile(t *testing.T){
	ownerRefs := []metav1.OwnerReference{
		{
			APIVersion: "v1",
			Kind:       "Test",
			Name:       "test",
			UID:        "123",
		},
	}

	inputDocument := `
			kind: ConfigMap
			apiVersion: v1
			metadata:
			  name: eighth
			data:
			  name: value
			---
			apiVersion: v1
			kind: Pod
			metadata:
			  name: example-test
			  annotations:
				"helm.sh/hook": test-success
	`

	baseEngineOutput := map[string]string{
		"template.yaml":  inputDocument,
	}

	engine := NewOwnerRefEngine(&mockEngine{out: baseEngineOutput}, ownerRefs)
	out, err := engine.Render(&chart.Chart{}, map[string]interface{}{})

	t.Log(out)
	t.Log(err)
}