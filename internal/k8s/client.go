// Package k8s 封装了所有跟 Kubernetes 打交道的逻辑。
package k8s

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"zey/internal/config"
)

// Client 封装一个 k8s 客户端 + 当前操作的命名空间。
// clientset 小写开头=私有,外部包不能直接访问,只能通过我们提供的方法。
type Client struct {
	clientset *kubernetes.Clientset
	namespace string
}

// Namespace 返回当前命名空间(公开 getter,因为 namespace 字段是私有的)。
func (c *Client) Namespace() string { return c.namespace }

// NewClient 根据配置构造客户端。nsOverride 非空时覆盖配置里的命名空间。
func NewClient(cfg *config.Config, nsOverride string) (*Client, error) {
	// 这套加载规则和 kubectl 完全一致:
	// 优先 ExplicitPath -> 再看 KUBECONFIG 环境变量 -> 最后 ~/.kube/config
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if cfg.Kubeconfig != "" {
		loadingRules.ExplicitPath = cfg.Kubeconfig
	}

	overrides := &clientcmd.ConfigOverrides{}
	if cfg.Context != "" {
		overrides.CurrentContext = cfg.Context
	}

	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("加载 kubeconfig 失败: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("创建 k8s 客户端失败: %w", err)
	}

	// 命名空间优先级:命令行 -n > 配置文件 > "default"
	ns := cfg.Namespace
	if nsOverride != "" {
		ns = nsOverride
	}
	if ns == "" {
		ns = "default"
	}

	return &Client{clientset: clientset, namespace: ns}, nil
}
