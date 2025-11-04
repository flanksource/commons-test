package helm

import "github.com/flanksource/clicky/exec"

type Kubectl func(args ...string) (*exec.ExecResult, error)

type Metadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	UID             string            `json:"uid,omitempty"`
	Annotations     map[string]string `json:"annotations"`
	Labels          map[string]string `json:"labels"`
	OwnerReferences []OwnerRef        `json:"ownerReferences"`
	Generation      int64             `json:"generation,omitempty"`
}

type Kind struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

type OwnerRef struct {
	Kind `json:",inline"`
	Name string `json:"name"`
	UID  string `json:"uid,omitempty"`
}

func (o OwnerRef) AsObject() Object {
	return Object{
		Metadata: Metadata{
			Name: o.Name,
			UID:  o.UID,
		},
		Kind: Kind{
			APIVersion: o.APIVersion,
			Kind:       o.Kind.Kind,
		},
	}
}

type Object struct {
	Metadata `json:"metadata,omitempty"`
	Kind     `json:",inline"`
}
