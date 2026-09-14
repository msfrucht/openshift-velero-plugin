package proxy

import (
	"context"
	"testing"

	"github.com/konveyor/openshift-velero-plugin/velero-plugins/clients"
	"github.com/konveyor/openshift-velero-plugin/velero-plugins/util/test"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	"github.com/vmware-tanzu/velero/pkg/plugin/velero"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	ktesting "k8s.io/client-go/testing"
)

type clientErrorType string

const (
	noClientError      clientErrorType = "NoError"
	forbiddenGetError  clientErrorType = "Forbidden"
	initError          clientErrorType = "InitError"
)

// newCoreV1Client returns a swappable clients.CoreV1Client function backed by a
// fake clientset. errorType controls which reactor (if any) is prepended for
// ConfigMap get calls. clientInitError is returned instead of the client itself,
// simulating a failure to initialise the Kubernetes client. startingConfigMap,
// when non-nil, is seeded into the fake on first call.
func newCoreV1Client(
	errorType clientErrorType,
	clientInitError error,
	startingConfigMap *corev1.ConfigMap,
) func() (corev1client.CoreV1Interface, error) {
	cs := k8sfake.NewSimpleClientset()

	switch errorType {
	case forbiddenGetError:
		cs.Fake.PrependReactor("get", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, k8serrors.NewForbidden(
				schema.GroupResource{Resource: "configmaps"}, action.(ktesting.GetAction).GetName(), nil,
			)
		})
	case noClientError:
		// no reactor — fake behaves normally
	}

	return func() (corev1client.CoreV1Interface, error) {
		if clientInitError != nil {
			return nil, clientInitError
		}
		c := cs.CoreV1()
		if startingConfigMap != nil {
			if _, err := c.ConfigMaps(startingConfigMap.Namespace).Create(
				context.Background(), startingConfigMap, metav1.CreateOptions{},
			); err != nil {
				return nil, err
			}
		}
		return c, nil
	}
}

func TestBackupPluginAppliesTo(t *testing.T) {
	plugin := &BackupPlugin{Log: test.NewLogger()}
	actual, err := plugin.AppliesTo()
	require.NoError(t, err)
	assert.Equal(t, velero.ResourceSelector{IncludedResources: []string{"proxies.config.openshift.io"}}, actual)
}

// proxyItem converts a configv1.Proxy into an unstructured item suitable for Execute.
func proxyItem(t *testing.T, proxy configv1.Proxy) *unstructured.Unstructured {
	t.Helper()
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&proxy)
	require.NoError(t, err)
	return &unstructured.Unstructured{Object: obj}
}

func TestBackupPluginExecute(t *testing.T) {
	tests := []struct {
		name                string
		input               configv1.Proxy
		startingConfigMap   *corev1.ConfigMap
		clientErrorType     clientErrorType
		clientInitError     error
		wantErr             bool
		wantItemName        string
		wantAdditionalItems []velero.ResourceIdentifier
	}{
		{
			name: "no TrustedCA set — no additional items",
			input: configv1.Proxy{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec:       configv1.ProxySpec{},
			},
			clientErrorType:     noClientError,
			wantErr:             false,
			wantItemName:        "cluster",
			wantAdditionalItems: []velero.ResourceIdentifier{},
		},
		{
			name: "TrustedCA references existing ConfigMap — included as additional item",
			input: configv1.Proxy{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.ProxySpec{
					TrustedCA: configv1.ConfigMapNameReference{Name: "user-ca-bundle"},
				},
			},
			startingConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "user-ca-bundle",
					Namespace: "openshift-config",
				},
			},
			clientErrorType: noClientError,
			wantErr:         false,
			wantItemName:    "cluster",
			wantAdditionalItems: []velero.ResourceIdentifier{
				{
					GroupResource: schema.GroupResource{Resource: "configmaps"},
					Namespace:     "openshift-config",
					Name:          "user-ca-bundle",
				},
			},
		},
		{
			name: "TrustedCA references missing ConfigMap — warning only, no error",
			input: configv1.Proxy{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.ProxySpec{
					TrustedCA: configv1.ConfigMapNameReference{Name: "missing-ca-bundle"},
				},
			},
			clientErrorType:     noClientError,
			wantErr:             false,
			wantItemName:        "cluster",
			wantAdditionalItems: []velero.ResourceIdentifier{},
		},
		{
			name: "non-TrustedCA proxy fields are preserved on the returned item",
			input: configv1.Proxy{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec:       configv1.ProxySpec{HTTPProxy: "http://proxy.example.com:3128"},
			},
			clientErrorType:     noClientError,
			wantErr:             false,
			wantItemName:        "cluster",
			wantAdditionalItems: []velero.ResourceIdentifier{},
		},
		{
			name: "client init failure returns error",
			input: configv1.Proxy{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.ProxySpec{
					TrustedCA: configv1.ConfigMapNameReference{Name: "user-ca-bundle"},
				},
			},
			clientErrorType: initError,
			clientInitError: assert.AnError,
			wantErr:         true,
		},
		{
			name: "forbidden get on ConfigMap returns error",
			input: configv1.Proxy{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.ProxySpec{
					TrustedCA: configv1.ConfigMapNameReference{Name: "user-ca-bundle"},
				},
			},
			clientErrorType:     forbiddenGetError,
			wantErr:             true,
			wantAdditionalItems: []velero.ResourceIdentifier{},
		},
	}

	originalCoreV1Client := clients.CoreV1Client
	t.Cleanup(func() { clients.CoreV1Client = originalCoreV1Client })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients.CoreV1Client = newCoreV1Client(tt.clientErrorType, tt.clientInitError, tt.startingConfigMap)

			plugin := &BackupPlugin{Log: test.NewLogger()}
			item := proxyItem(t, tt.input)

			resultItem, additionalItems, err := plugin.Execute(item, &velerov1.Backup{})

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, resultItem)

			name, found, err := unstructured.NestedString(resultItem.UnstructuredContent(), "metadata", "name")
			assert.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, tt.wantItemName, name)
			assert.ElementsMatch(t, tt.wantAdditionalItems, additionalItems)
		})
	}
}
