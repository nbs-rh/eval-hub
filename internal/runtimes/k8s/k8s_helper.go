package k8s

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"text/template"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// KubernetesManifestsDir is the directory under which YAML manifest files are looked up (relative to working directory).
const KubernetesManifestsDir = "config/kubernetes"

// KubernetesHelper wraps the Kubernetes client-go client and exposes methods to interact with the cluster.
// Keeping this abstraction in one place allows all call sites to stay unchanged if we switch
// to a different underlying Kubernetes client implementation.
type KubernetesHelper struct {
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface
	restMapper    *restmapper.DeferredDiscoveryRESTMapper
}

// NewKubernetesHelper builds a Kubernetes client (in-cluster config, then default kubeconfig)
// and returns a KubernetesHelper. Call this when LocalMode is false.
func NewKubernetesHelper() (*KubernetesHelper, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules,
			configOverrides,
		).ClientConfig()
		if err != nil {
			return nil, err
		}
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	/*
	DiscoveryClient = “ask the cluster what APIs/resources exist.”
	RESTMapper = “use that to map Kind → resource name and scope.”
	Dynamic client = “create this object using that resource name and scope.”
	*/
	// used to create arbitrary resources from YAML.
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	// used to map group/version/kind to REST API endpoints. 
	/*
	memory.NewMemCacheClient is from k8s.io/client-go/discovery/cached/memory. 
	“MemCache” here means an in-memory cache (in-process), not the Memcached server.
	What it does:
	- NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc)) wraps the discovery client in a small in-memory cache so that:
		- Group/version/resource lookups don’t hit the API server every time.
		- Repeated REST mappings are served from memory.
	*/
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))
	
	return &KubernetesHelper{
		clientset:     clientset,
		dynamicClient: dynamicClient,
		restMapper:    restMapper,
	}, nil
}

// CreateConfigMap creates a ConfigMap in the given namespace.
// name is the ConfigMap name; data is the key-value map for ConfigMap.Data.
// opts may be nil; use it to set labels and annotations.
func (h *KubernetesHelper) CreateConfigMap(
	ctx context.Context,
	namespace, name string,
	data map[string]string,
	opts *CreateConfigMapOptions,
) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Data: data,
	}
	if opts != nil {
		if len(opts.Labels) > 0 {
			cm.ObjectMeta.Labels = opts.Labels
		}
		if len(opts.Annotations) > 0 {
			cm.ObjectMeta.Annotations = opts.Annotations
		}
	}
	return h.clientset.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
}

// CreateConfigMapOptions holds optional metadata for CreateConfigMap.
type CreateConfigMapOptions struct {
	Labels      map[string]string
	Annotations map[string]string
}

// CreateResourceFromFile loads a YAML manifest from config/kubernetes/<filename>, replaces
// placeholders like {{ .ProviderID }} with values from placeholders (key "ProviderID" -> value),
// and creates the resource in the cluster. placeholders may be nil.
// Returns the created resource as *unstructured.Unstructured.
func (h *KubernetesHelper) CreateResourceFromFile(
	ctx context.Context,
	filename string,
	placeholders map[string]string,
) (*unstructured.Unstructured, error) {
	cleanPath := filepath.Clean(filename)
+	if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(os.PathSeparator)) {
+		return nil, fmt.Errorf("invalid manifest filename: %q", filename)
+	}
+	path := filepath.Join(KubernetesManifestsDir, cleanPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New(filename).Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	data := make(map[string]string)
	if placeholders != nil {
		data = placeholders
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	yamlBytes := buf.Bytes()

	decoder := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	obj := &unstructured.Unstructured{}
	_, _, err = decoder.Decode(yamlBytes, nil, obj)
	if err != nil {
		return nil, err
	}

	gvk := obj.GroupVersionKind()
	mapping, err := h.restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}

	var result *unstructured.Unstructured
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		result, err = h.dynamicClient.Resource(mapping.Resource).Namespace(obj.GetNamespace()).Create(ctx, obj, metav1.CreateOptions{})
	} else {
		result, err = h.dynamicClient.Resource(mapping.Resource).Create(ctx, obj, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}
