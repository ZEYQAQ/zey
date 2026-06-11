package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"zey/internal/nginxinstall"
)

func newNginxInstallCmd() *cobra.Command {
	inst := &nginxinstall.Installer{}
	cmd := &cobra.Command{
		Use:   "nginxInstall",
		Short: "从源码编译安装 nginx,配置 stub_status 并用 systemd 启动",
		Long: `从源码编译安装 nginx(预设一套模块与路径),写好 nginx.conf
(含 stub_status 监控端点)与 systemd 服务,daemon-reload + enable + 启动。

源码包默认用打包进 zey 的内置版本(nginx-1.31.1),无需联网或上传。
也可 --source 指定本地 .tar.gz,或 --version 从 nginx.org 下载。

注意:需要 Linux + root(要装依赖、make install、写 systemd)。
可加 --dry-run 在任意系统预览全部步骤、configure 参数、nginx.conf 与 unit。`,
		Example: "  sudo zey nginxInstall                            # 用内置源码包,直接装\n" +
			"  sudo zey nginxInstall --source ./nginx-x.y.z.tar.gz   # 用指定的本地包\n" +
			"  sudo zey nginxInstall --version 1.27.4               # 从 nginx.org 下载\n" +
			"  zey nginxInstall --dry-run                           # 预览,不实际安装",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			inst.Out = os.Stdout
			return inst.Run()
		},
	}
	cmd.Flags().StringVar(&inst.Source, "source", "", "改用指定的本地源码包 .tar.gz(默认用内置版本)")
	cmd.Flags().StringVar(&inst.Version, "version", "", "改从 nginx.org 下载该版本(默认用内置版本)")
	cmd.Flags().IntVar(&inst.StubStatusPort, "stub-status-port", 8081, "stub_status 监听端口")
	cmd.Flags().BoolVar(&inst.SkipDeps, "skip-deps", false, "跳过装编译依赖")
	cmd.Flags().BoolVar(&inst.DryRun, "dry-run", false, "只预览,不实际安装")
	return cmd
}
