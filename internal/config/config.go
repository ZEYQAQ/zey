// Package config 负责 zey 自己的配置(怎么连 k8s),持久化到 ~/.zey/config.json。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config 保存连接 k8s 所需的信息。
// 结构体字段后面反引号里的是 tag,JSON 序列化时用作字段名;
// omitempty 表示该字段为空值时不写进 JSON。
type Config struct {
	Kubeconfig string `json:"kubeconfig,omitempty"` // kubeconfig 路径,空=用默认 ~/.kube/config
	Context    string `json:"context,omitempty"`    // kubeconfig 里的 context 名
	Namespace  string `json:"namespace,omitempty"`  // 默认命名空间
}

// Dir 返回配置目录 ~/.zey
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户主目录失败: %w", err)
	}
	return filepath.Join(home, ".zey"), nil
}

// Path 返回配置文件完整路径 ~/.zey/config.json
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// ExportsDir 返回存档目录 ~/.zey/exports,export 默认把文件写到这里。
func ExportsDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "exports"), nil
}

// Load 读取配置。文件不存在时返回一个空配置(而不是报错),
// 这样用户第一次用还没配置过也能正常跑。
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	return &c, nil
}

// Save 把配置写回磁盘,目录不存在会自动创建。
// 这是一个「指针接收者」方法 (c *Config),能读到 c 的最新内容。
func (c *Config) Save() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ") // 带缩进,方便人读
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil { // 0600: 只有自己可读写
		return fmt.Errorf("写入配置失败: %w", err)
	}
	return nil
}
