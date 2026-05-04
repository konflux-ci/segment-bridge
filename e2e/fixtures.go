package e2e

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"
)

// PipelineRun represents a simplified Tekton PipelineRun structure
type PipelineRun struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		UID       string            `json:"uid"`
		Labels    map[string]string `json:"labels,omitempty"`
	} `json:"metadata"`
	Status struct {
		StartTime      string `json:"startTime,omitempty"`
		CompletionTime string `json:"completionTime,omitempty"`
		Conditions     []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"conditions,omitempty"`
		ChildReferences []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"childReferences,omitempty"`
	} `json:"status"`
}

// TaskRun represents a simplified Tekton TaskRun structure
type TaskRun struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		UID       string `json:"uid"`
	} `json:"metadata"`
}

// TektonResultsRecord represents a record from Tekton Results API
type TektonResultsRecord struct {
	Name string `json:"name"`
	Data struct {
		Type  string `json:"type"`
		Value string `json:"value"` // base64-encoded JSON
	} `json:"data"`
}

// TektonResultsResponse represents the response from tkn-results list
type TektonResultsResponse struct {
	Records []TektonResultsRecord `json:"records"`
}

// NewPipelineRun creates a PipelineRun with sensible defaults
func NewPipelineRun(name, namespace, uid string) *PipelineRun {
	now := time.Now().UTC()
	pr := &PipelineRun{
		APIVersion: "tekton.dev/v1",
		Kind:       "PipelineRun",
	}
	pr.Metadata.Name = name
	pr.Metadata.Namespace = namespace
	pr.Metadata.UID = uid
	pr.Metadata.Labels = make(map[string]string)
	pr.Status.StartTime = now.Add(-5 * time.Minute).Format(time.RFC3339)
	pr.Status.CompletionTime = now.Format(time.RFC3339)
	pr.Status.Conditions = []struct {
		Type   string `json:"type"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	}{
		{Type: "Succeeded", Status: "True", Reason: "Succeeded"},
	}
	return pr
}

// WithLabels adds labels to the PipelineRun
func (pr *PipelineRun) WithLabels(labels map[string]string) *PipelineRun {
	for k, v := range labels {
		pr.Metadata.Labels[k] = v
	}
	return pr
}

// WithStatus sets the status reason
func (pr *PipelineRun) WithStatus(reason string) *PipelineRun {
	if len(pr.Status.Conditions) > 0 {
		pr.Status.Conditions[0].Reason = reason
		pr.Status.Conditions[0].Status = "True"
		if reason == "Failed" {
			pr.Status.Conditions[0].Status = "False"
		}
	}
	return pr
}

// WithChildReferences adds child task references
func (pr *PipelineRun) WithChildReferences(count int) *PipelineRun {
	pr.Status.ChildReferences = make([]struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}, count)
	for i := 0; i < count; i++ {
		pr.Status.ChildReferences[i].Name = pr.Metadata.Name + "-task-" + strconv.Itoa(i)
		pr.Status.ChildReferences[i].Type = "TaskRun"
	}
	return pr
}

// NewTaskRun creates a TaskRun
func NewTaskRun(name, namespace, uid string) *TaskRun {
	tr := &TaskRun{
		APIVersion: "tekton.dev/v1",
		Kind:       "TaskRun",
	}
	tr.Metadata.Name = name
	tr.Metadata.Namespace = namespace
	tr.Metadata.UID = uid
	return tr
}

// EncodeTektonResultsRecord encodes a PipelineRun or TaskRun into a Tekton Results record
func EncodeTektonResultsRecord(obj interface{}, recordName string) (TektonResultsRecord, error) {
	jsonData, err := json.Marshal(obj)
	if err != nil {
		return TektonResultsRecord{}, err
	}

	var recordType string
	switch obj.(type) {
	case *PipelineRun:
		recordType = "tekton.dev/v1.PipelineRun"
	case *TaskRun:
		recordType = "tekton.dev/v1.TaskRun"
	default:
		recordType = "unknown"
	}

	record := TektonResultsRecord{
		Name: recordName,
	}
	record.Data.Type = recordType
	record.Data.Value = base64.StdEncoding.EncodeToString(jsonData)

	return record, nil
}

// CreateTektonResultsResponse creates a full Tekton Results API response
func CreateTektonResultsResponse(records []TektonResultsRecord) (string, error) {
	response := TektonResultsResponse{
		Records: records,
	}
	jsonData, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(jsonData), nil
}

// Namespace represents a simplified Kubernetes Namespace structure
type Namespace struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name              string            `json:"name"`
		UID               string            `json:"uid"`
		CreationTimestamp string            `json:"creationTimestamp"`
		Labels            map[string]string `json:"labels,omitempty"`
	} `json:"metadata"`
}

// NewNamespace creates a Namespace with sensible defaults
func NewNamespace(name, uid, creationTimestamp string) *Namespace {
	ns := &Namespace{
		APIVersion: "v1",
		Kind:       "Namespace",
	}
	ns.Metadata.Name = name
	ns.Metadata.UID = uid
	ns.Metadata.CreationTimestamp = creationTimestamp
	ns.Metadata.Labels = make(map[string]string)
	return ns
}

// WithLabels adds labels to the Namespace
func (ns *Namespace) WithLabels(labels map[string]string) *Namespace {
	for k, v := range labels {
		ns.Metadata.Labels[k] = v
	}
	return ns
}

// Component represents a simplified AppStudio Component structure
type Component struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name              string `json:"name"`
		Namespace         string `json:"namespace"`
		UID               string `json:"uid"`
		CreationTimestamp string `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Application string `json:"application"`
	} `json:"spec"`
}

// NewComponent creates a Component with sensible defaults
func NewComponent(name, namespace, uid, creationTimestamp, application string) *Component {
	comp := &Component{
		APIVersion: "appstudio.redhat.com/v1alpha1",
		Kind:       "Component",
	}
	comp.Metadata.Name = name
	comp.Metadata.Namespace = namespace
	comp.Metadata.UID = uid
	comp.Metadata.CreationTimestamp = creationTimestamp
	comp.Spec.Application = application
	return comp
}

// KonfluxCR represents a simplified Konflux operator CR structure
type KonfluxCR struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name              string `json:"name"`
		Namespace         string `json:"namespace"`
		UID               string `json:"uid"`
		CreationTimestamp string `json:"creationTimestamp"`
	} `json:"metadata"`
	Status struct {
		Conditions []struct {
			Type               string `json:"type"`
			Status             string `json:"status"`
			Reason             string `json:"reason"`
			LastTransitionTime string `json:"lastTransitionTime"`
		} `json:"conditions,omitempty"`
	} `json:"status"`
}

// NewKonfluxCR creates a Konflux CR with sensible defaults
func NewKonfluxCR(uid, namespace, creationTimestamp string) *KonfluxCR {
	cr := &KonfluxCR{
		APIVersion: "konflux.konflux-ci.dev/v1alpha1",
		Kind:       "Konflux",
	}
	cr.Metadata.Name = "konflux"
	cr.Metadata.Namespace = namespace
	cr.Metadata.UID = uid
	cr.Metadata.CreationTimestamp = creationTimestamp
	return cr
}

// WithReadyCondition adds a Ready condition to the Konflux CR
func (cr *KonfluxCR) WithReadyCondition(status, reason, lastTransitionTime string) *KonfluxCR {
	cr.Status.Conditions = append(cr.Status.Conditions, struct {
		Type               string `json:"type"`
		Status             string `json:"status"`
		Reason             string `json:"reason"`
		LastTransitionTime string `json:"lastTransitionTime"`
	}{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		LastTransitionTime: lastTransitionTime,
	})
	return cr
}

// CreateNamespaceListJSON creates a Kubernetes namespace list JSON
func CreateNamespaceListJSON(namespaces []*Namespace) (string, error) {
	list := map[string]interface{}{
		"items": namespaces,
	}
	data, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// CreateComponentListJSON creates an AppStudio component list JSON
func CreateComponentListJSON(components []*Component) (string, error) {
	list := map[string]interface{}{
		"items": components,
	}
	data, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// CreateKonfluxCRJSON creates a Konflux CR JSON
func CreateKonfluxCRJSON(cr *KonfluxCR) (string, error) {
	data, err := json.Marshal(cr)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
