package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// ExportCounts 记录导出了多少个各类资源。
type ExportCounts struct {
	Services    int
	Deployments int
	ConfigMaps  int
}

// Export 把命名空间内的 Service / Deployment / ConfigMap 导出成多文档 YAML 写入 w。
// allNamespaces=true 时跨所有命名空间。
func (c *Client) Export(ctx context.Context, w io.Writer, allNamespaces bool) (ExportCounts, error) {
	var counts ExportCounts
	ns := c.namespace
	if allNamespaces {
		ns = "" // client-go 里空字符串表示「所有命名空间」
	}

	// ---- Services ----
	svcList, err := c.clientset.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return counts, fmt.Errorf("列出 Service 失败: %w", err)
	}
	for i := range svcList.Items {
		svc := svcList.Items[i]
		// 类型客户端返回的对象 TypeMeta 往往是空的,导出前手动补上 apiVersion/kind。
		svc.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}
		cleanMeta(&svc.ObjectMeta)
		svc.Status = corev1.ServiceStatus{} // status 是运行时状态,导出没意义
		svc.Spec.ClusterIP = ""             // 清掉集群分配的 IP,才能 apply 到别的集群
		svc.Spec.ClusterIPs = nil
		if err := writeYAMLDoc(w, &svc); err != nil {
			return counts, err
		}
		counts.Services++
	}

	// ---- Deployments ----
	depList, err := c.clientset.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return counts, fmt.Errorf("列出 Deployment 失败: %w", err)
	}
	for i := range depList.Items {
		dep := depList.Items[i]
		dep.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}
		cleanMeta(&dep.ObjectMeta)
		dep.Status = appsv1.DeploymentStatus{}
		if err := writeYAMLDoc(w, &dep); err != nil {
			return counts, err
		}
		counts.Deployments++
	}

	// ---- ConfigMaps ----
	cmList, err := c.clientset.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return counts, fmt.Errorf("列出 ConfigMap 失败: %w", err)
	}
	for i := range cmList.Items {
		cm := cmList.Items[i]
		if cm.Name == "kube-root-ca.crt" {
			continue // k8s 自动注入的根证书,跳过
		}
		cm.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}
		cleanMeta(&cm.ObjectMeta)
		if err := writeYAMLDoc(w, &cm); err != nil {
			return counts, err
		}
		counts.ConfigMaps++
	}

	return counts, nil
}

// cleanMeta 去掉那些只在运行时有意义、会妨碍重新 apply 的字段。
func cleanMeta(m *metav1.ObjectMeta) {
	m.ResourceVersion = ""
	m.UID = ""
	m.Generation = 0
	m.CreationTimestamp = metav1.Time{}
	m.ManagedFields = nil
	m.SelfLink = ""
	m.OwnerReferences = nil
	delete(m.Annotations, "kubectl.kubernetes.io/last-applied-configuration")
	delete(m.Annotations, "deployment.kubernetes.io/revision")
	if len(m.Annotations) == 0 {
		m.Annotations = nil
	}
}

// writeYAMLDoc 把一个对象序列化成 YAML,并以 "---" 作为多文档分隔符写出。
func writeYAMLDoc(w io.Writer, obj interface{}) error {
	// sigs.k8s.io/yaml 会先转成 JSON(尊重 json tag)再转 YAML,
	// 所以字段名和 kubectl 输出一致。
	data, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Errorf("序列化 YAML 失败: %w", err)
	}
	_, err = fmt.Fprintf(w, "---\n%s", data)
	return err
}

// ApplyResult 记录 apply 时每个资源的处理结果。
type ApplyResult struct {
	Kind   string
	Name   string
	Action string // created / updated
}

// Apply 读取多文档 YAML,把其中的 Service/Deployment/ConfigMap 创建或更新到集群。
func (c *Client) Apply(ctx context.Context, r io.Reader) ([]ApplyResult, error) {
	var results []ApplyResult
	// 这个 decoder 能逐个读取 "---" 分隔的 YAML 文档,并自动把 YAML 转成 JSON。
	decoder := utilyaml.NewYAMLOrJSONDecoder(r, 4096)
	for {
		var raw runtime.RawExtension
		if err := decoder.Decode(&raw); err != nil {
			if err == io.EOF {
				break // 读完了
			}
			return results, fmt.Errorf("解析 YAML 失败: %w", err)
		}
		if len(raw.Raw) == 0 {
			continue // 跳过空文档
		}

		// 技巧:先只解析出 Kind 这一个字段,再决定用哪个具体类型来反序列化。
		var tm metav1.TypeMeta
		if err := json.Unmarshal(raw.Raw, &tm); err != nil {
			return results, fmt.Errorf("识别资源类型失败: %w", err)
		}

		var res ApplyResult
		var err error
		switch tm.Kind {
		case "Service":
			var svc corev1.Service
			if err = json.Unmarshal(raw.Raw, &svc); err != nil {
				return results, err
			}
			res, err = c.applyService(ctx, &svc)
		case "Deployment":
			var dep appsv1.Deployment
			if err = json.Unmarshal(raw.Raw, &dep); err != nil {
				return results, err
			}
			res, err = c.applyDeployment(ctx, &dep)
		case "ConfigMap":
			var cm corev1.ConfigMap
			if err = json.Unmarshal(raw.Raw, &cm); err != nil {
				return results, err
			}
			res, err = c.applyConfigMap(ctx, &cm)
		default:
			continue // 不认识的类型直接跳过
		}
		if err != nil {
			return results, fmt.Errorf("应用 %s/%s 失败: %w", tm.Kind, res.Name, err)
		}
		results = append(results, res)
	}
	return results, nil
}

// nsOf 返回对象自带的命名空间,没有就用客户端默认的。
func (c *Client) nsOf(objNamespace string) string {
	if objNamespace != "" {
		return objNamespace
	}
	return c.namespace
}

func (c *Client) applyService(ctx context.Context, svc *corev1.Service) (ApplyResult, error) {
	res := ApplyResult{Kind: "Service", Name: svc.Name}
	api := c.clientset.CoreV1().Services(c.nsOf(svc.Namespace))
	existing, err := api.Get(ctx, svc.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, svc, metav1.CreateOptions{})
		res.Action = "created"
		return res, err
	} else if err != nil {
		return res, err
	}
	// 更新必须带上当前 resourceVersion;ClusterIP 不可变,沿用旧值。
	svc.ResourceVersion = existing.ResourceVersion
	svc.Spec.ClusterIP = existing.Spec.ClusterIP
	svc.Spec.ClusterIPs = existing.Spec.ClusterIPs
	_, err = api.Update(ctx, svc, metav1.UpdateOptions{})
	res.Action = "updated"
	return res, err
}

func (c *Client) applyDeployment(ctx context.Context, dep *appsv1.Deployment) (ApplyResult, error) {
	res := ApplyResult{Kind: "Deployment", Name: dep.Name}
	api := c.clientset.AppsV1().Deployments(c.nsOf(dep.Namespace))
	existing, err := api.Get(ctx, dep.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, dep, metav1.CreateOptions{})
		res.Action = "created"
		return res, err
	} else if err != nil {
		return res, err
	}
	dep.ResourceVersion = existing.ResourceVersion
	_, err = api.Update(ctx, dep, metav1.UpdateOptions{})
	res.Action = "updated"
	return res, err
}

func (c *Client) applyConfigMap(ctx context.Context, cm *corev1.ConfigMap) (ApplyResult, error) {
	res := ApplyResult{Kind: "ConfigMap", Name: cm.Name}
	api := c.clientset.CoreV1().ConfigMaps(c.nsOf(cm.Namespace))
	existing, err := api.Get(ctx, cm.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, cm, metav1.CreateOptions{})
		res.Action = "created"
		return res, err
	} else if err != nil {
		return res, err
	}
	cm.ResourceVersion = existing.ResourceVersion
	_, err = api.Update(ctx, cm, metav1.UpdateOptions{})
	res.Action = "updated"
	return res, err
}
