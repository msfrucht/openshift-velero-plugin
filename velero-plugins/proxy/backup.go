package proxy

import (
	"context"
	"encoding/json"

	"github.com/konveyor/openshift-velero-plugin/velero-plugins/clients"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/sirupsen/logrus"
	v1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	"github.com/vmware-tanzu/velero/pkg/plugin/velero"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// BackupPlugin is a backup item action plugin for Velero.
type BackupPlugin struct {
	Log logrus.FieldLogger
}

// AppliesTo returns a velero.ResourceSelector that applies to configmaps.
func (p *BackupPlugin) AppliesTo() (velero.ResourceSelector, error) {
	return velero.ResourceSelector{
		IncludedResources: []string{"proxies.config.openshift.io"},
	}, nil
}

// Execute collects the trusted CA ConfigMap referenced by a cluster Proxy object
// as an additional backup item so that it is included in the backup.
func (p *BackupPlugin) Execute(item runtime.Unstructured, backup *v1.Backup) (runtime.Unstructured, []velero.ResourceIdentifier, error) {
	p.Log.Info("[proxy-backup] Entering proxy backup plugin")

	clusterProxy := &configv1.Proxy{}
	itemMarshal, err := json.Marshal(item)
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(itemMarshal, clusterProxy); err != nil {
		return nil, nil, err
	}
	p.Log.Infof("[proxy-backup] Proxy: %v", clusterProxy.Name)

	var additionalItems []velero.ResourceIdentifier

	// check for any rootCAs configmap reference.
	// The reference is implicitly always to namespace openshift-config;
	// that namespace is non-configurable.
	// https://docs.redhat.com/en/documentation/openshift_container_platform/4.22/html/config_apis/proxy-config-openshift-io-v1
	if clusterProxy.Spec.TrustedCA.Name != "" {
		additionalItems, err = p.addConfigMapRef(clusterProxy.Spec.TrustedCA.Name, additionalItems)
		if err != nil {
			return nil, nil, err
		}
	}

	var out map[string]interface{}
	objrec, err := json.Marshal(clusterProxy)
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(objrec, &out); err != nil {
		return nil, nil, err
	}
	item.SetUnstructuredContent(out)
	return item, additionalItems, nil
}

// Determine if there is a ConfigMap that needs to be added as velero reference and add it if found.
func (p *BackupPlugin) addConfigMapRef(name string, additionalItems []velero.ResourceIdentifier) ([]velero.ResourceIdentifier, error) {
	ref, err := p.getTrustedCAObjectReference(name)
	if err != nil {
		return additionalItems, err
	}
	if ref != nil {
		additionalItems = append(additionalItems, *ref)
	}
	return additionalItems, nil
}

// Retrieve the ConfigMap that contains the trusted CAs and return the resource identifier.
// If the ConfigMap is missing a warning is issued instead of an error.
func (p *BackupPlugin) getTrustedCAObjectReference(name string) (*velero.ResourceIdentifier, error) {
	client, err := clients.CoreV1Client()
	if err != nil {
		return nil, err
	}

	cm, err := client.ConfigMaps("openshift-config").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			p.Log.Warnf("[proxy-backup] ConfigMap openshift-config/%s not found: %s", name, err.Error())
			return nil, nil
		}
		p.Log.Errorf("[proxy-backup] Error adding ConfigMap openshift-config/%s to proxy backup: %s", name, err.Error())
		return nil, err
	}

	return &velero.ResourceIdentifier{
		GroupResource: schema.GroupResource{Resource: "configmaps"},
		Namespace:     cm.Namespace,
		Name:          cm.Name,
	}, nil
}
