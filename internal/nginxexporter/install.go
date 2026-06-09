// Package nginxexporter 负责在本机安装 nginx-prometheus-exporter,
// 并注册成 systemd 服务(开机自启 + 失败自动重启)。
package nginxexporter

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	serviceName     = "nginx-exporter"                          // systemd 服务名
	unitPath        = "/etc/systemd/system/nginx-exporter.service"
	binaryName      = "nginx-prometheus-exporter"               // 压缩包里二进制的名字
	fallbackVersion = "1.4.2"                                   // 取不到最新版时的兜底版本
	releaseAPI      = "https://api.github.com/repos/nginx/nginx-prometheus-exporter/releases/latest"
)

// Installer 持有一次安装所需的全部配置。
type Installer struct {
	Version    string    // "latest" 或具体版本如 "1.4.2"
	Listen     string    // 监听地址,如 ":9113"
	ScrapeURI  string    // nginx stub_status 地址
	InstallDir string    // 二进制安装目录,如 /usr/local/bin
	DryRun     bool      // 只预览不执行
	Out        io.Writer // 输出目标,默认 os.Stdout
}

// Run 执行完整安装流程。
func (i *Installer) Run() error {
	if i.Out == nil {
		i.Out = os.Stdout
	}

	// 预检:真实安装必须在 Linux + root 下进行;dry-run 跳过,方便任意系统预览。
	if !i.DryRun {
		if runtime.GOOS != "linux" {
			return fmt.Errorf("该命令依赖 systemd,只能在 Linux 上运行(当前系统是 %s);可加 --dry-run 预览将执行的操作", runtime.GOOS)
		}
		if os.Geteuid() != 0 {
			return fmt.Errorf("需要 root 权限,请用 sudo 运行:sudo zey nginxExporterInit")
		}
		if _, err := exec.LookPath("systemctl"); err != nil {
			return fmt.Errorf("未找到 systemctl,本机似乎没有 systemd: %w", err)
		}
	}

	ver, err := i.resolveVersion()
	if err != nil {
		return err
	}
	arch := runtime.GOARCH // amd64 / arm64
	url := fmt.Sprintf(
		"https://github.com/nginx/nginx-prometheus-exporter/releases/download/v%s/nginx-prometheus-exporter_%s_linux_%s.tar.gz",
		ver, ver, arch,
	)
	binPath := filepath.Join(i.InstallDir, binaryName)
	unit := i.unitFile(binPath)

	// dry-run:打印计划后直接返回。
	if i.DryRun {
		i.printPlan(ver, url, binPath, unit)
		return nil
	}

	// 1) 下载并安装二进制
	fmt.Fprintf(i.Out, "下载 %s ...\n", url)
	if err := downloadAndInstall(url, binPath); err != nil {
		return err
	}
	fmt.Fprintf(i.Out, "已安装二进制: %s\n", binPath)

	// 2) 写 systemd unit
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("写入 systemd unit 失败: %w", err)
	}
	fmt.Fprintf(i.Out, "已写入 unit: %s\n", unitPath)

	// 3) 重新加载 systemd 并设置开机自启 + 立即启动
	if out, err := run("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload 失败: %v\n%s", err, out)
	}
	if out, err := run("systemctl", "enable", "--now", serviceName); err != nil {
		return fmt.Errorf("启用并启动服务失败: %v\n%s", err, out)
	}

	// 4) 输出结果(本机 IP+端口、systemd 状态)
	i.printResult()
	return nil
}

// resolveVersion 决定要安装的版本:指定了就用指定的,否则查 GitHub 最新。
func (i *Installer) resolveVersion() (string, error) {
	v := strings.TrimSpace(i.Version)
	if v != "" && v != "latest" {
		return strings.TrimPrefix(v, "v"), nil
	}
	tag, err := latestReleaseTag()
	if err != nil {
		fmt.Fprintf(i.Out, "获取最新版本失败(%v),改用兜底版本 %s\n", err, fallbackVersion)
		return fallbackVersion, nil
	}
	return strings.TrimPrefix(tag, "v"), nil
}

// latestReleaseTag 调 GitHub API 取最新 release 的 tag(如 "v1.4.2")。
func latestReleaseTag() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, releaseAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API 返回 HTTP %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("未取到 tag_name")
	}
	return rel.TagName, nil
}

// downloadAndInstall 下载 tar.gz 并把里面的二进制解压到 destPath。
func downloadAndInstall(url, destPath string) error {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败,HTTP %d: %s", resp.StatusCode, url)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("创建安装目录失败: %w", err)
	}
	return extractBinary(resp.Body, binaryName, destPath)
}

// extractBinary 从 tar.gz 流里找到名为 wantName 的文件,写到 destPath(0755)。
// 先写临时文件再 rename,避免覆盖正在运行的旧二进制时出问题。
func extractBinary(r io.Reader, wantName, destPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("解压 gzip 失败: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("压缩包里没找到 %s", wantName)
		}
		if err != nil {
			return fmt.Errorf("读取 tar 失败: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != wantName {
			continue
		}
		tmp := destPath + ".tmp"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return os.Rename(tmp, destPath)
	}
}

// unitFile 生成 systemd unit 文件内容。
func (i *Installer) unitFile(binPath string) string {
	return fmt.Sprintf(`[Unit]
Description=NGINX Prometheus Exporter (installed by zey)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
DynamicUser=yes
ExecStart=%s --nginx.scrape-uri=%s --web.listen-address=%s
Restart=on-failure
RestartSec=5s
# 安全加固
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
`, binPath, i.ScrapeURI, i.Listen)
}

// printResult 安装成功后,输出:i) 本机 IP+端口  ii) systemd 状态。
func (i *Installer) printResult() {
	port := portOf(i.Listen)
	ip := primaryIP()

	fmt.Fprintln(i.Out, "\n========================================")
	fmt.Fprintln(i.Out, " nginx exporter 安装完成 ✅")
	fmt.Fprintln(i.Out, "========================================")

	// i) 本机 IP 和端口
	fmt.Fprintln(i.Out, "\n【访问地址】")
	if ip != "" {
		fmt.Fprintf(i.Out, "  本机 IP : %s\n", ip)
		fmt.Fprintf(i.Out, "  端口    : %s\n", port)
		fmt.Fprintf(i.Out, "  指标地址: http://%s:%s/metrics\n", ip, port)
	} else {
		fmt.Fprintf(i.Out, "  端口    : %s(未能自动识别本机 IP)\n", port)
	}
	if others := allIPv4(); len(others) > 0 {
		fmt.Fprintf(i.Out, "  本机所有 IPv4: %s\n", strings.Join(others, ", "))
	}

	// ii) systemd 状态
	fmt.Fprintln(i.Out, "\n【systemd 状态】")
	if active, _ := run("systemctl", "is-active", serviceName); active != "" {
		fmt.Fprintf(i.Out, "  is-active : %s", active) // 输出自带换行
	}
	if enabled, _ := run("systemctl", "is-enabled", serviceName); enabled != "" {
		fmt.Fprintf(i.Out, "  is-enabled: %s", enabled)
	}
	status, _ := run("systemctl", "status", serviceName, "--no-pager")
	fmt.Fprintln(i.Out, "\n--- systemctl status "+serviceName+" ---")
	fmt.Fprintln(i.Out, strings.TrimRight(status, "\n"))
}

// printPlan 在 dry-run 模式下打印将要执行的操作和 unit 内容。
func (i *Installer) printPlan(ver, url, binPath, unit string) {
	fmt.Fprintln(i.Out, "[dry-run] 仅预览,不会实际安装。将执行:")
	fmt.Fprintf(i.Out, "  1) 下载: %s\n", url)
	fmt.Fprintf(i.Out, "  2) 安装二进制到: %s\n", binPath)
	fmt.Fprintf(i.Out, "  3) 写 systemd unit: %s\n", unitPath)
	fmt.Fprintln(i.Out, "  4) systemctl daemon-reload")
	fmt.Fprintf(i.Out, "  5) systemctl enable --now %s\n", serviceName)

	fmt.Fprintln(i.Out, "\n--- 将生成的 systemd unit ---")
	fmt.Fprint(i.Out, unit)

	fmt.Fprintln(i.Out, "\n--- 安装完成后将输出 ---")
	port := portOf(i.Listen)
	ip := primaryIP()
	if ip == "" {
		ip = "<本机IP>"
	}
	fmt.Fprintf(i.Out, "  本机 IP: %s  端口: %s  →  http://%s:%s/metrics\n", ip, port, ip, port)
	fmt.Fprintf(i.Out, "  以及 systemctl status %s 的运行状态\n", serviceName)
}

// run 执行外部命令,返回合并后的输出(stdout+stderr)。
func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// portOf 从监听地址里取端口,如 ":9113" -> "9113"。
func portOf(listen string) string {
	if _, port, err := net.SplitHostPort(listen); err == nil && port != "" {
		return port
	}
	return strings.TrimPrefix(listen, ":")
}

// primaryIP 取本机用于对外通信的首选 IP(不真正发包,只看路由选了哪个网卡)。
func primaryIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return ""
}

// allIPv4 列出本机所有非回环 IPv4 地址。
func allIPv4() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ips []string
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if v4 := ipnet.IP.To4(); v4 != nil {
				ips = append(ips, v4.String())
			}
		}
	}
	return ips
}
