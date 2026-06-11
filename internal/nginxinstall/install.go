// Package nginxinstall 从源码编译安装 nginx,写好 stub_status 配置与 systemd 服务并启动。
package nginxinstall

import (
	_ "embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// embeddedNginxSrc 是打进二进制的 nginx 源码包,作为默认源(无需联网或 --source)。
//
//go:embed assets/nginx-1.31.1.tar.gz
var embeddedNginxSrc []byte

// embeddedNginxVersion 是内置源码包的版本。
const embeddedNginxVersion = "1.31.1"

// Installer 持有一次 nginx 源码安装所需的配置。
type Installer struct {
	Source         string    // 本地源码包 .tar.gz 路径
	Version        string    // 无本地包时,从 nginx.org 下载的版本
	StubStatusPort int       // stub_status 监听端口(默认 8081)
	SkipDeps       bool      // 跳过 yum 装依赖
	DryRun         bool      // 只预览不执行
	Out            io.Writer // 输出目标
}

// 编译依赖(与脚本一致)
var nginxBuildDeps = []string{
	"gcc", "make", "pcre-devel", "perl-devel", "perl-ExtUtils-Embed",
	"zlib-devel", "libxml2", "libxml2-devel", "libxslt-devel",
	"openssl-devel", "gd-devel",
}

// configure 参数(与脚本一致)
var nginxConfigureArgs = []string{
	"--prefix=/usr/local/nginx",
	"--sbin-path=/usr/sbin/nginx",
	"--modules-path=/usr/lib64/nginx/modules",
	"--conf-path=/etc/nginx/nginx.conf",
	"--pid-path=/var/run/nginx.pid",
	"--error-log-path=/var/log/nginx/error.log",
	"--http-log-path=/var/log/nginx/access.log",
	"--with-file-aio",
	"--with-http_ssl_module",
	"--with-http_v2_module",
	"--with-http_realip_module",
	"--with-stream_ssl_preread_module",
	"--with-http_addition_module",
	"--with-http_xslt_module=dynamic",
	"--with-http_image_filter_module=dynamic",
	"--with-http_sub_module",
	"--with-http_dav_module",
	"--with-http_flv_module",
	"--with-http_mp4_module",
	"--with-http_gunzip_module",
	"--with-http_gzip_static_module",
	"--with-http_random_index_module",
	"--with-http_secure_link_module",
	"--with-http_degradation_module",
	"--with-http_slice_module",
	"--with-http_stub_status_module",
	"--with-http_perl_module=dynamic",
	"--with-http_auth_request_module",
	"--with-mail=dynamic",
	"--with-mail_ssl_module",
	"--with-pcre",
	"--with-pcre-jit",
	"--with-stream=dynamic",
	"--with-stream_ssl_module",
	"--with-debug",
}

// nginx.conf 模板,__STUB_PORT__ 会被替换成实际端口。
const nginxConfTemplate = `user  nginx;
worker_processes  auto;

error_log  /var/log/nginx/error.log warn;
pid        /var/run/nginx.pid;

# 如需使用 stream / mail / perl 等动态模块,取消下行注释
# load_module /usr/lib64/nginx/modules/ngx_stream_module.so;

events {
    worker_connections  10240;
    use epoll;
}

http {
    include       /etc/nginx/mime.types;
    default_type  application/octet-stream;

    log_format  main  '$remote_addr - $remote_user [$time_local] "$request" '
                      '$status $body_bytes_sent "$http_referer" '
                      '"$http_user_agent" "$http_x_forwarded_for" '
                      '$request_time $upstream_response_time';

    access_log  /var/log/nginx/access.log  main;

    sendfile            on;
    tcp_nopush          on;
    keepalive_timeout   65;
    gzip                on;

    include /etc/nginx/conf.d/*.conf;

    # stub_status 监控端点(供 nginx exporter 采集)
    server {
        listen       __STUB_PORT__;
        server_name  localhost;

        location /stub_status {
            stub_status;
            access_log off;
            allow 127.0.0.1;
            deny all;
        }
    }
}
`

// systemd unit(与脚本一致)
const nginxSystemdUnit = `[Unit]
Description=nginx - high performance web server
Documentation=http://nginx.org/en/docs/
After=network-online.target remote-fs.target nss-lookup.target
Wants=network-online.target

[Service]
Type=forking
PIDFile=/var/run/nginx.pid
ExecStartPre=/usr/sbin/nginx -t -c /etc/nginx/nginx.conf
ExecStart=/usr/sbin/nginx -c /etc/nginx/nginx.conf
ExecReload=/usr/sbin/nginx -s reload
ExecStop=/bin/kill -s QUIT $MAINPID
KillSignal=SIGQUIT
TimeoutStopSec=5
KillMode=mixed
PrivateTmp=true
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
`

const buildRoot = "/tmp/zey-nginx-build"

// Run 执行完整的编译安装流程。
func (i *Installer) Run() error {
	if i.Out == nil {
		i.Out = os.Stdout
	}
	if i.StubStatusPort == 0 {
		i.StubStatusPort = 8081
	}

	if !i.DryRun {
		if runtime.GOOS != "linux" {
			return fmt.Errorf("该命令需要编译环境与 systemd,只能在 Linux 上运行(当前 %s);可加 --dry-run 预览", runtime.GOOS)
		}
		if os.Geteuid() != 0 {
			return fmt.Errorf("需要 root 权限(要装依赖、make install、写 systemd),请用 sudo 运行")
		}
	}

	pkgMgr := detectPkgMgr()
	conf := strings.ReplaceAll(nginxConfTemplate, "__STUB_PORT__", strconv.Itoa(i.StubStatusPort))

	local, err := i.explicitSource()
	if err != nil {
		return err
	}

	if i.DryRun {
		i.printPlan(local, pkgMgr, conf)
		return nil
	}

	// 准备构建目录
	if err := os.RemoveAll(buildRoot); err != nil {
		return fmt.Errorf("清理构建目录失败: %w", err)
	}
	if err := os.MkdirAll(buildRoot, 0o755); err != nil {
		return err
	}

	// 确定源码包:本地优先,否则按版本下载
	var tarball string
	switch {
	case local != "":
		tarball, _ = filepath.Abs(local)
	case i.Version != "":
		url := nginxDownloadURL(i.Version)
		tarball = filepath.Join(buildRoot, fmt.Sprintf("nginx-%s.tar.gz", i.Version))
		fmt.Fprintf(i.Out, "下载 %s ...\n", url)
		if err := download(url, tarball); err != nil {
			return err
		}
	default:
		// 默认:用打包进二进制的内置源码包,无需联网或 --source
		tarball = filepath.Join(buildRoot, "nginx-"+embeddedNginxVersion+".tar.gz")
		fmt.Fprintf(i.Out, "使用内置源码包 nginx-%s(已打包进 zey)\n", embeddedNginxVersion)
		if err := os.WriteFile(tarball, embeddedNginxSrc, 0o644); err != nil {
			return fmt.Errorf("释放内置源码包失败: %w", err)
		}
	}

	// [1/8] 解压
	fmt.Fprintln(i.Out, "\n[1/8] 解压源码 ...")
	if err := i.runStream("", "tar", "-xzf", tarball, "-C", buildRoot); err != nil {
		return fmt.Errorf("解压失败: %w", err)
	}
	srcDir, err := findSourceDir(buildRoot)
	if err != nil {
		return err
	}
	fmt.Fprintf(i.Out, "源码目录: %s\n", srcDir)

	// [2/8] 依赖
	if i.SkipDeps {
		fmt.Fprintln(i.Out, "\n[2/8] 跳过依赖安装(--skip-deps)")
	} else {
		fmt.Fprintln(i.Out, "\n[2/8] 安装编译依赖 ...")
		if err := i.runStream("", pkgMgr, append([]string{"install", "-y"}, nginxBuildDeps...)...); err != nil {
			return fmt.Errorf("安装依赖失败: %w", err)
		}
	}

	// [3/8] configure
	fmt.Fprintln(i.Out, "\n[3/8] configure ...")
	if err := i.runStream(srcDir, filepath.Join(srcDir, "configure"), nginxConfigureArgs...); err != nil {
		return fmt.Errorf("configure 失败: %w", err)
	}

	// [4/8] make  [5/8] make install
	fmt.Fprintln(i.Out, "\n[4/8] make ...")
	if err := i.runStream(srcDir, "make"); err != nil {
		return fmt.Errorf("make 失败: %w", err)
	}
	fmt.Fprintln(i.Out, "\n[5/8] make install ...")
	if err := i.runStream(srcDir, "make", "install"); err != nil {
		return fmt.Errorf("make install 失败: %w", err)
	}

	// [6/8] 用户、日志目录、配置
	fmt.Fprintln(i.Out, "\n[6/8] 创建用户、日志目录、写配置 ...")
	_, _ = run("useradd", "-r", "-s", "/sbin/nologin", "nginx") // 已存在则忽略错误
	_ = os.MkdirAll("/var/log/nginx", 0o755)
	_, _ = run("chown", "-R", "nginx:nginx", "/var/log/nginx")

	_ = os.MkdirAll("/etc/nginx", 0o755)
	if _, err := os.Stat("/etc/nginx/nginx.conf"); err == nil {
		_ = os.Rename("/etc/nginx/nginx.conf", "/etc/nginx/nginx.conf.bak")
		fmt.Fprintln(i.Out, "  已备份原配置 -> /etc/nginx/nginx.conf.bak")
	}
	if err := os.WriteFile("/etc/nginx/nginx.conf", []byte(conf), 0o644); err != nil {
		return fmt.Errorf("写 nginx.conf 失败: %w", err)
	}
	_ = os.MkdirAll("/etc/nginx/conf.d", 0o755)

	// [7/8] systemd
	fmt.Fprintln(i.Out, "\n[7/8] 注册 systemd 服务 ...")
	if err := os.WriteFile("/etc/systemd/system/nginx.service", []byte(nginxSystemdUnit), 0o644); err != nil {
		return fmt.Errorf("写 systemd unit 失败: %w", err)
	}
	if err := i.runStream("", "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := i.runStream("", "systemctl", "enable", "nginx"); err != nil {
		return err
	}

	// [8/8] 校验并启动
	fmt.Fprintln(i.Out, "\n[8/8] 校验配置并启动 ...")
	if err := i.runStream("", "nginx", "-t"); err != nil {
		return fmt.Errorf("nginx -t 配置校验失败: %w", err)
	}
	if err := i.runStream("", "systemctl", "restart", "nginx"); err != nil {
		return fmt.Errorf("启动 nginx 失败: %w", err)
	}

	i.printResult()
	return nil
}

// explicitSource 返回用户用 --source 显式指定的源码包;没指定返回 ("", nil)。
func (i *Installer) explicitSource() (string, error) {
	if i.Source == "" {
		return "", nil
	}
	if _, err := os.Stat(i.Source); err != nil {
		return "", fmt.Errorf("指定的源码包不存在: %s", i.Source)
	}
	return i.Source, nil
}

// printPlan 在 dry-run 模式打印将执行的全部步骤与生成的文件。
func (i *Installer) printPlan(local, pkgMgr, conf string) {
	var srcDesc, srcDir string
	switch {
	case local != "":
		abs, _ := filepath.Abs(local)
		srcDesc = "本地源码包: " + abs
		name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(local), ".tar.gz"), ".tgz")
		srcDir = filepath.Join(buildRoot, name)
	case i.Version != "":
		srcDesc = "下载: " + nginxDownloadURL(i.Version)
		srcDir = filepath.Join(buildRoot, "nginx-"+i.Version)
	default:
		srcDesc = fmt.Sprintf("内置源码包 nginx-%s(已打包进 zey,%s)", embeddedNginxVersion, humanSize(len(embeddedNginxSrc)))
		srcDir = filepath.Join(buildRoot, "nginx-"+embeddedNginxVersion)
	}

	fmt.Fprintln(i.Out, "[dry-run] 仅预览,不会实际安装。将执行:")
	fmt.Fprintf(i.Out, "  源码来源 : %s\n", srcDesc)
	fmt.Fprintf(i.Out, "  解压到   : %s\n", buildRoot)
	if i.SkipDeps {
		fmt.Fprintln(i.Out, "  装依赖   : 跳过(--skip-deps)")
	} else {
		fmt.Fprintf(i.Out, "  装依赖   : %s install -y %s\n", pkgMgr, strings.Join(nginxBuildDeps, " "))
	}
	fmt.Fprintf(i.Out, "  stub端口 : %d\n", i.StubStatusPort)

	fmt.Fprintln(i.Out, "\n--- configure 命令 ---")
	fmt.Fprintf(i.Out, "%s/configure \\\n    %s\n", srcDir, strings.Join(nginxConfigureArgs, " \\\n    "))
	fmt.Fprintln(i.Out, "\n然后: make && make install")

	fmt.Fprintln(i.Out, "\n--- 将写入 /etc/nginx/nginx.conf ---")
	fmt.Fprint(i.Out, conf)
	fmt.Fprintln(i.Out, "\n--- 将写入 /etc/systemd/system/nginx.service ---")
	fmt.Fprint(i.Out, nginxSystemdUnit)
	fmt.Fprintln(i.Out, "\n最后: systemctl daemon-reload && enable && nginx -t && restart,并输出本机 IP、stub_status、systemd 状态")
}

// printResult 安装成功后输出版本、stub_status 探测、本机 IP、systemd 状态。
func (i *Installer) printResult() {
	fmt.Fprintln(i.Out, "\n========================================")
	fmt.Fprintln(i.Out, " nginx 安装完成 ✅")
	fmt.Fprintln(i.Out, "========================================")

	if v, _ := run("nginx", "-v"); v != "" {
		fmt.Fprintf(i.Out, "版本: %s", strings.TrimRight(v, "\n")+"\n")
	}
	fmt.Fprintln(i.Out, "二进制: /usr/sbin/nginx    配置: /etc/nginx/nginx.conf")

	// stub_status 探测
	fmt.Fprintf(i.Out, "\n【stub_status】(供 nginx exporter 采集,仅本机可访问)\n")
	url := fmt.Sprintf("http://127.0.0.1:%d/stub_status", i.StubStatusPort)
	fmt.Fprintf(i.Out, "  地址: %s\n", url)
	if body, err := httpGetBody(url); err == nil {
		fmt.Fprintf(i.Out, "  探测:\n%s\n", indent(body, "    "))
	} else {
		fmt.Fprintf(i.Out, "  探测失败(服务可能还在启动): %v\n", err)
	}
	if out, _ := run("nginx", "-V"); strings.Contains(out, "http_stub_status_module") {
		fmt.Fprintln(i.Out, "  模块: http_stub_status_module 已编译 ✅")
	}

	if ip := primaryIP(); ip != "" {
		fmt.Fprintf(i.Out, "\n本机 IP: %s\n", ip)
	}

	fmt.Fprintln(i.Out, "\n【systemd 状态】")
	if active, _ := run("systemctl", "is-active", "nginx"); active != "" {
		fmt.Fprintf(i.Out, "  is-active : %s", active)
	}
	if enabled, _ := run("systemctl", "is-enabled", "nginx"); enabled != "" {
		fmt.Fprintf(i.Out, "  is-enabled: %s", enabled)
	}
	status, _ := run("systemctl", "status", "nginx", "--no-pager")
	fmt.Fprintln(i.Out, "\n--- systemctl status nginx ---")
	fmt.Fprintln(i.Out, strings.TrimRight(status, "\n"))
}

// --- 小工具 ---

func (i *Installer) runStream(dir, name string, args ...string) error {
	fmt.Fprintf(i.Out, "$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = i.Out
	cmd.Stderr = i.Out
	return cmd.Run()
}

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func detectPkgMgr() string {
	for _, m := range []string{"yum", "dnf"} {
		if _, err := exec.LookPath(m); err == nil {
			return m
		}
	}
	return "yum"
}

func findSourceDir(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(root, e.Name(), "configure")); err == nil {
				return filepath.Join(root, e.Name()), nil
			}
		}
	}
	return "", fmt.Errorf("解压后未找到含 configure 的源码目录")
}

func nginxDownloadURL(version string) string {
	return fmt.Sprintf("https://nginx.org/download/nginx-%s.tar.gz", version)
}

func download(url, dest string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败 HTTP %d: %s", resp.StatusCode, url)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func httpGetBody(url string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for idx := range lines {
		lines[idx] = prefix + lines[idx]
	}
	return strings.Join(lines, "\n")
}

func humanSize(n int) string {
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

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
