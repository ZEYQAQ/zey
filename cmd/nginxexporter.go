package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"zey/internal/nginxexporter"
)

func newNginxExporterInitCmd() *cobra.Command {
	inst := &nginxexporter.Installer{}
	cmd := &cobra.Command{
		Use:   "nginxExporterInit",
		Short: "一键安装 nginx-prometheus-exporter 并用 systemd 开机自启",
		Long: `下载并安装 nginx-prometheus-exporter,注册为 systemd 服务
(开机自启、异常自动重启),由 systemd 持续维护。

完成后输出:
  i)  本机 IP 和 exporter 监听端口
  ii) 该服务的 systemd 状态

注意:依赖 systemd,需在 Linux 上以 root 运行(sudo)。
可加 --dry-run 在任意系统预览将执行的操作与生成的 unit 文件。`,
		Example: "  sudo zey nginxExporterInit\n" +
			"  sudo zey nginxExporterInit --listen :9113 --scrape-uri http://127.0.0.1:8081/stub_status\n" +
			"  zey nginxExporterInit --dry-run",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			inst.Out = os.Stdout
			return inst.Run()
		},
	}
	cmd.Flags().StringVar(&inst.Version, "version", "latest", "exporter 版本,latest=取 GitHub 最新")
	cmd.Flags().StringVar(&inst.Listen, "listen", ":9113", "exporter 监听地址")
	cmd.Flags().StringVar(&inst.ScrapeURI, "scrape-uri", "http://127.0.0.1:8081/stub_status", "nginx stub_status 地址")
	cmd.Flags().StringVar(&inst.InstallDir, "install-dir", "/usr/local/bin", "二进制安装目录")
	cmd.Flags().BoolVar(&inst.DryRun, "dry-run", false, "只预览将执行的操作,不实际安装")
	return cmd
}
