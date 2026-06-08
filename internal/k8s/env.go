package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

// workload 是对三种工作负载(Deployment/StatefulSet/DaemonSet)的统一抽象。
// 这是 Go 里很常见的手法:用一个小结构体 + 闭包,把「不同类型但操作相似」的东西抹平。
//   - podSpec 是指向对象内部 PodSpec 的指针,改它就等于改了原对象
//   - save 是一个闭包,负责把改完的对象提交回 k8s(每种类型用不同的 API)
type workload struct {
	kind    string
	podSpec *corev1.PodSpec
	save    func(ctx context.Context) error
}

// normalizeKind 把各种简写统一成标准名,支持 deploy/sts/ds 这类缩写。
func normalizeKind(kind string) string {
	switch strings.ToLower(kind) {
	case "deploy", "deployment", "deployments":
		return "deployment"
	case "sts", "statefulset", "statefulsets":
		return "statefulset"
	case "ds", "daemonset", "daemonsets":
		return "daemonset"
	default:
		return ""
	}
}

// getWorkload 按类型拉取工作负载,封装成统一的 workload。
func (c *Client) getWorkload(ctx context.Context, kind, name string) (*workload, error) {
	switch normalizeKind(kind) {
	case "deployment":
		obj, err := c.clientset.AppsV1().Deployments(c.namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return &workload{
			kind:    "Deployment",
			podSpec: &obj.Spec.Template.Spec,
			save: func(ctx context.Context) error {
				_, err := c.clientset.AppsV1().Deployments(c.namespace).Update(ctx, obj, metav1.UpdateOptions{})
				return err
			},
		}, nil
	case "statefulset":
		obj, err := c.clientset.AppsV1().StatefulSets(c.namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return &workload{
			kind:    "StatefulSet",
			podSpec: &obj.Spec.Template.Spec,
			save: func(ctx context.Context) error {
				_, err := c.clientset.AppsV1().StatefulSets(c.namespace).Update(ctx, obj, metav1.UpdateOptions{})
				return err
			},
		}, nil
	case "daemonset":
		obj, err := c.clientset.AppsV1().DaemonSets(c.namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return &workload{
			kind:    "DaemonSet",
			podSpec: &obj.Spec.Template.Spec,
			save: func(ctx context.Context) error {
				_, err := c.clientset.AppsV1().DaemonSets(c.namespace).Update(ctx, obj, metav1.UpdateOptions{})
				return err
			},
		}, nil
	default:
		return nil, fmt.Errorf("不支持的类型 %q(支持 deployment/statefulset/daemonset)", kind)
	}
}

// ContainerEnv 表示某个容器的环境变量,用于把结果返回给上层打印。
type ContainerEnv struct {
	Container string
	Vars      []corev1.EnvVar
}

// GetEnv 读取工作负载下每个容器的环境变量。
func (c *Client) GetEnv(ctx context.Context, kind, name string) ([]ContainerEnv, error) {
	wl, err := c.getWorkload(ctx, kind, name)
	if err != nil {
		return nil, err
	}
	// make([]T, 0, n):预分配容量 n 的空 slice,避免 append 时反复扩容。
	result := make([]ContainerEnv, 0, len(wl.podSpec.Containers))
	for _, ctr := range wl.podSpec.Containers {
		result = append(result, ContainerEnv{Container: ctr.Name, Vars: ctr.Env})
	}
	return result, nil
}

// SetEnv 立即修改环境变量。set 是要新增/覆盖的键值,unset 是要删除的键。
// 用 RetryOnConflict 包一层:如果提交时发现别人也改了这个对象(版本冲突),
// 会自动重新拉取最新版本再改一遍 —— 这是 k8s 编程里非常标准的乐观锁重试。
func (c *Client) SetEnv(ctx context.Context, kind, name, container string, set map[string]string, unset []string) ([]string, error) {
	var changed []string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		wl, err := c.getWorkload(ctx, kind, name) // 每次重试都重新拉最新版
		if err != nil {
			return err
		}
		changed = applyEnvChanges(wl.podSpec, container, set, unset)
		if len(changed) == 0 {
			return nil // 没有任何改动,不必提交
		}
		return wl.save(ctx)
	})
	return changed, err
}

// applyEnvChanges 在内存里修改 PodSpec 各容器的环境变量,返回改动说明列表。
func applyEnvChanges(spec *corev1.PodSpec, containerFilter string, set map[string]string, unset []string) []string {
	// map 的遍历顺序是随机的,为了输出稳定,先把 key 排序。
	setKeys := make([]string, 0, len(set))
	for k := range set {
		setKeys = append(setKeys, k)
	}
	sort.Strings(setKeys)

	var changes []string
	for i := range spec.Containers {
		ctr := &spec.Containers[i] // 必须取指针,否则改的是副本,改不到原 slice
		if containerFilter != "" && ctr.Name != containerFilter {
			continue // 指定了容器名就只改它
		}
		for _, k := range setKeys {
			ctr.Env = upsertEnv(ctr.Env, k, set[k])
			changes = append(changes, fmt.Sprintf("[%s] %s=%s", ctr.Name, k, set[k]))
		}
		for _, k := range unset {
			before := len(ctr.Env)
			ctr.Env = removeEnv(ctr.Env, k)
			if len(ctr.Env) != before {
				changes = append(changes, fmt.Sprintf("[%s] 删除 %s", ctr.Name, k))
			}
		}
	}
	return changes
}

// upsertEnv:存在同名变量就覆盖其值,否则追加一个。
func upsertEnv(env []corev1.EnvVar, key, value string) []corev1.EnvVar {
	for i := range env {
		if env[i].Name == key {
			env[i].Value = value
			env[i].ValueFrom = nil // 改成字面值,清掉原来的引用(如果有的话)
			return env
		}
	}
	return append(env, corev1.EnvVar{Name: key, Value: value})
}

// removeEnv:按名字删除变量。这里用了「原地过滤」技巧:env[:0] 复用底层数组。
func removeEnv(env []corev1.EnvVar, key string) []corev1.EnvVar {
	out := env[:0]
	for _, e := range env {
		if e.Name != key {
			out = append(out, e)
		}
	}
	return out
}
