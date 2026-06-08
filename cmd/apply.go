package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"zey/internal/config"
)

func newApplyCmd() *cobra.Command {
	var filename string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "把存档文件里的资源创建/更新到集群",
		Long: `读取存档 YAML,把其中的 Service / Deployment / ConfigMap 应用到集群。

与 export 对称:默认从存档目录 ~/.zey/exports/ 里挑最新的一份应用;
也可以用 -f 指定具体文件,或 -f - 从标准输入读取。
应用前会打印用的是哪个存档文件。`,
		Example: "  zey apply                  # 应用 ~/.zey/exports/ 里最新的存档(默认)\n  zey apply -f backup.yaml   # 指定文件\n  cat backup.yaml | zey apply -f -",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}

			var r io.Reader
			if filename == "-" {
				fmt.Println("从标准输入读取...")
				r = os.Stdin
			} else {
				path := filename
				if path == "" {
					// 没指定文件:自动找存档目录里最新的一份(对称于 export 的默认存档)
					path, err = latestExportFile()
					if err != nil {
						return err
					}
				}
				f, err := os.Open(path)
				if err != nil {
					return fmt.Errorf("打开文件失败: %w", err)
				}
				defer f.Close()
				r = f
				abs, _ := filepath.Abs(path)
				fmt.Printf("正在应用存档: %s\n", abs) // 打印用的是哪个存档文件
			}

			results, err := client.Apply(context.Background(), r)
			if err != nil {
				return err
			}
			if len(results) == 0 {
				fmt.Println("没有可应用的资源")
				return nil
			}
			fmt.Println("应用结果:")
			for _, res := range results {
				fmt.Printf("  %s/%s %s\n", res.Kind, res.Name, res.Action)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&filename, "filename", "f", "", "存档文件;留空=用 ~/.zey/exports/ 里最新一份;'-'=标准输入")
	return cmd
}

// latestExportFile 返回存档目录里修改时间最新的 zey-export-*.yaml。
func latestExportFile() (string, error) {
	dir, err := config.ExportsDir()
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("存档目录 %s 不存在,请先执行 zey export,或用 -f 指定文件", dir)
		}
		return "", err
	}

	var newest string
	var newestMod int64 = -1
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "zey-export-") || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if mod := info.ModTime().UnixNano(); mod > newestMod {
			newest = filepath.Join(dir, name)
			newestMod = mod
		}
	}
	if newest == "" {
		return "", fmt.Errorf("存档目录 %s 里没有 zey-export-*.yaml,请先执行 zey export,或用 -f 指定文件", dir)
	}
	return newest, nil
}
