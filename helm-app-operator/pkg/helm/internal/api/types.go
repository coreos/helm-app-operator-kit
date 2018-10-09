package api

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/helm/pkg/proto/hapi/release"
)

type ResourcePhase string

const (
	PhaseNone     ResourcePhase = ""
	PhaseApplying ResourcePhase = "Applying"
	PhaseApplied  ResourcePhase = "Applied"
	PhaseFailed   ResourcePhase = "Failed"
)

type ConditionReason string

const (
	ReasonUnknown               ConditionReason = "Unknown"
	ReasonCustomResourceAdded   ConditionReason = "CustomResourceAdded"
	ReasonCustomResourceUpdated ConditionReason = "CustomResourceUpdated"
	ReasonApplySuccessful       ConditionReason = "ApplySuccessful"
	ReasonApplyFailed           ConditionReason = "ApplyFailed"
)

type HelmAppStatus struct {
	Release            *release.Release `json:"release"`
	Phase              ResourcePhase    `json:"phase"`
	Reason             ConditionReason  `json:"reason,omitempty"`
	Message            string           `json:"message,omitempty"`
	LastUpdateTime     metav1.Time      `json:"lastUpdateTime,omitempty"`
	LastTransitionTime metav1.Time      `json:"lastTransitionTime,omitempty"`
}

func (s *HelmAppStatus) ToMap() (map[string]interface{}, error) {
	var out map[string]interface{}
	jsonObj, err := json.Marshal(&s)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(jsonObj, &out)
	return out, nil
}

// SetPhase takes a custom resource status and returns the updated status, without updating the resource in the cluster.
func (s *HelmAppStatus) SetPhase(phase ResourcePhase, reason ConditionReason, message string) *HelmAppStatus {
	s.LastUpdateTime = metav1.Now()
	if s.Phase != phase {
		s.Phase = phase
		s.LastTransitionTime = metav1.Now()
	}
	s.Message = message
	s.Reason = reason
	return s
}

// SetRelease takes a release object and adds or updates the release on the status object
func (s *HelmAppStatus) SetRelease(release *release.Release) *HelmAppStatus {
	s.Release = release
	return s
}

// StatusFor safely returns a typed status block from a custom resource.
func StatusFor(cr *unstructured.Unstructured) *HelmAppStatus {
	switch cr.Object["status"].(type) {
	case *HelmAppStatus:
		return cr.Object["status"].(*HelmAppStatus)
	case map[string]interface{}:
		var status *HelmAppStatus
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(cr.Object["status"].(map[string]interface{}), &status); err != nil {
			return &HelmAppStatus{
				Phase:   PhaseFailed,
				Reason:  ReasonApplyFailed,
				Message: err.Error(),
			}
		}
		return status
	default:
		return &HelmAppStatus{}
	}
}
