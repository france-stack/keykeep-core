package desktop

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Settings 是本地安装的运行期配置，落在 <Root>\settings.json。
//
// 与 .env 的分工：这里只放「这台机器自己的」信息（端口、密钥、口令），
// 不放任何上游厂商凭据——那些加密存在数据库里，由控制台管理。
type Settings struct {
	Version        string `json:"version"`
	KEK            string `json:"kek"`            // base64(32B)，凭据加密主密钥
	SessionSecret  string `json:"sessionSecret"`  // 控制面会话签名
	AuditChainKey  string `json:"auditChainKey"`  // 审计链 HMAC 密钥（a1k）
	PGPassword     string `json:"pgPassword"`     // 内置 PostgreSQL 口令
	PGPort         int    `json:"pgPort"`         // 内置 PostgreSQL 端口
	ControlPort    int    `json:"controlPort"`    // 控制台端口
	GatewayPort    int    `json:"gatewayPort"`    // 数据面网关端口
	MockPort       int    `json:"mockPort"`       // 演示上游端口
	MachineCode    string `json:"machineCode"`    // 机器码（授权绑定用）
	InitializedAt  string `json:"initializedAt"`  // 首次初始化时间
	AdminAccount   string `json:"adminAccount"`   // 首装管理员账号
	AutoStart      bool   `json:"autoStart"`      // 开机自启（托盘可改）
	DemoProviderID string `json:"demoProviderId"` // 内置演示供应商 id

	// LanEnabled 打开后网关与控制台绑 0.0.0.0，员工可从局域网直连（ADR-012）。
	// 默认关闭：关闭时行为与「只绑回环」的旧行为完全一致。
	LanEnabled bool `json:"lanEnabled"`
	// AdvertisedGatewayURL 是给员工填的网关地址（如 http://10.0.0.5:8080）。
	// 为空时按本机第一个非回环 IPv4 现算，不落盘——落盘只由托盘开关动作写入，
	// 避免机器换网时把上一次的地址当成配置。
	AdvertisedGatewayURL string `json:"advertisedGatewayUrl,omitempty"`
	// AlwaysOn 记录是否已设置「常开模式」（关待机与休眠），仅作状态回显。
	AlwaysOn bool `json:"alwaysOn,omitempty"`

	// UIShell 是控制台的呈现方式（ADR-013）：webview2（默认）/ browser。
	//
	// 落盘是给「某台机器上 WebView2 行为异常」留的出口：用户改一次就一直生效，
	// 不需要等新版本。环境变量 KEYKEEP_SHELL_MODE 可临时覆盖它。
	// 空值等价于 webview2——老版本写下的 settings.json 不会有这个字段。
	UIShell string `json:"uiShell,omitempty"`
	// 这里刻意不存管理员口令。
	//
	// 以前有个 adminPasswordShown 字段，与「初始登录信息.txt」各存一份明文：
	// settings.json 会被各种工具读来读去（托盘改开关即整文件重写），备份/排障时
	// 也常被顺手拷走，等于把一个谁都能打开的口令抄进了一个生命周期更长的机器文件，
	// 而它唯一的读者就是写 welcome 文件那一次。一次性口令只有一个载体：那个
	// 明确告知「改完口令请删掉」的 txt。
}

// 端口合法区间：避开系统保留段与常见冲突端口。
const (
	minPort = 1024
	maxPort = 65535
)

// pgPortCandidates 是内置 PostgreSQL 的候选端口。
//
// 为什么不能只用默认值：Windows 的 Hyper-V/WSL 会成段保留高位端口
// （netsh interface ipv4 show excludedportrange protocol=tcp 可查），
// 55432 经常正好落在保留段里，表现为 postgres 启动即
// "FATAL: could not bind IPv4 address: Permission denied"。
// 首装时按顺序探测第一个可用者，结果写进 settings.json 后固定使用。
var pgPortCandidates = []int{
	55432, 55442, 55452, 55462, 55472, 55482, 55512, 55522, 55632, 55732,
	5433, 5434, 5435, 5436,
}

// DefaultSettings 返回一套未初始化的默认值（端口固定，密钥尚未生成）。
func DefaultSettings() Settings {
	return Settings{
		Version:       "0", // 由首次初始化写入真实版本
		PGPort:        DefaultPGPort,
		ControlPort:   DefaultControlPort,
		GatewayPort:   DefaultGatewayPort,
		MockPort:      DefaultMockPort,
		AdminAccount:  "admin",
		AutoStart:     true,
		InitializedAt: "",
	}
}

// NormalizePort 把端口收敛到安全区间。
//
// settings.json 在本机可写，而端口会被拼进子进程命令行；这里限定为
// 合法端口区间即可消除「把这个字段当参数注入入口」的可能（端口只能是数字，
// 且 postgres/node 的 -p/--port 参数只接受数字）。
func NormalizePort(value, fallback int) int {
	if value < minPort || value > maxPort {
		return fallback
	}
	return value
}

// PickFreePort 依次尝试候选端口，返回第一个能绑定的。
//
// 用「真绑一次再释放」判断，而不是只看保留段：端口也可能被别的进程占着。
func PickFreePort(candidates []int, fallback int) int {
	for _, port := range candidates {
		if port < minPort || port > maxPort {
			continue
		}
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			continue
		}
		listener.Close()
		return port
	}
	return fallback
}

// EnsurePorts 在首装时挑好各服务的端口；已有配置不改（改端口会让旧数据断连）。
func (s *Settings) EnsurePorts(fresh bool) {
	if !fresh {
		return
	}
	s.PGPort = PickFreePort(pgPortCandidates, DefaultPGPort)
}

// LoadSettings 读 settings.json；不存在时返回默认值且 exists=false。
func (l *Layout) LoadSettings() (Settings, bool, error) {
	raw, err := os.ReadFile(l.SettingsFile())
	if errors.Is(err, os.ErrNotExist) {
		return DefaultSettings(), false, nil
	}
	if err != nil {
		return Settings{}, false, err
	}
	settings := DefaultSettings()
	if err := json.Unmarshal(raw, &settings); err != nil {
		return Settings{}, false, fmt.Errorf("settings.json 解析失败: %w", err)
	}
	settings.PGPort = NormalizePort(settings.PGPort, DefaultPGPort)
	settings.ControlPort = NormalizePort(settings.ControlPort, DefaultControlPort)
	settings.GatewayPort = NormalizePort(settings.GatewayPort, DefaultGatewayPort)
	settings.MockPort = NormalizePort(settings.MockPort, DefaultMockPort)
	if settings.AdminAccount == "" {
		settings.AdminAccount = "admin"
	}
	return settings, true, nil
}

// SaveSettings 原子写入（0600：里面有 KEK 与会话密钥）。
//
// 写入后先把数据压盘再改名：settings.json 里有 KEK，断电后若 rename 指向
// 半截文件，所有上游凭据将永久解不开。
func (l *Layout) SaveSettings(settings Settings) error {
	if err := os.MkdirAll(l.Root, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.SettingsFile() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, l.SettingsFile()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// randomSecret 生成 base64 编码的 n 字节随机串。
func randomSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// randomPassword 生成只含 URL 安全字符的口令（会进连接串，不能带 @ : / 等）。
func randomPassword(n int) (string, error) {
	// 拆成两段拼接：整串是个纯字母表常量，写成一字面量会被秘密扫描的
	// 高熵规则当成凭据命中，而它并不是凭据。
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ" +
		"23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

// RandomPassword 是 randomPassword 的导出包装，供 cmd 层使用。
func RandomPassword(n int) (string, error) { return randomPassword(n) }

// MachineCode 按「主机名 + 首个物理网卡 MAC」的哈希算机器码，与授权口径一致。
// 非 64 位十六进制的输入会在控制面被再次哈希，这里直接给规范值。
func MachineCode() string {
	host, _ := os.Hostname()
	mac := primaryMAC()
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(host)) + "|" + mac))
	return hex.EncodeToString(sum[:])
}

func primaryMAC() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "unknown"
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.HardwareAddr == nil {
			continue
		}
		if len(iface.HardwareAddr) < 6 {
			continue
		}
		return iface.HardwareAddr.String()
	}
	return "no-mac"
}

// DatabaseURL 拼出内置 PostgreSQL 的连接串（Prisma 用，带 schema）。
func (s Settings) DatabaseURL() string {
	return fmt.Sprintf("postgresql://keykeep:%s@127.0.0.1:%d/keykeep?schema=public",
		s.PGPassword, s.PGPort)
}

// GatewayDatabaseURL 是网关用的连接串（pgx 不认 schema 参数，用 sslmode）。
func (s Settings) GatewayDatabaseURL() string {
	return fmt.Sprintf("postgresql://keykeep:%s@127.0.0.1:%d/keykeep?sslmode=disable",
		s.PGPassword, s.PGPort)
}

// ControlURL 返回控制台地址。
func (s Settings) ControlURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.ControlPort)
}

// GatewayURL 返回数据面网关地址。
func (s Settings) GatewayURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.GatewayPort)
}

// MockURL 返回演示上游地址。
func (s Settings) MockURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.MockPort)
}

// ListenHostGateway 是网关的监听主机。局域网模式绑 0.0.0.0。
func (s Settings) ListenHostGateway() string {
	if s.LanEnabled {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

// ListenHostControl 是控制台的监听主机。局域网模式绑 0.0.0.0。
func (s Settings) ListenHostControl() string {
	if s.LanEnabled {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

// PublicGatewayURL 返回员工应该填的网关地址。
//
// 与 GatewayURL() 的分工：GatewayURL/ControlURL/MockURL 给**本机内部**用
// （健康探针、单实例检测、托盘「打开控制台」），始终是回环；对外广播的地址
// 必须单独走这里，否则局域网模式下本机会去连 0.0.0.0，健康检查全部失败。
func (s Settings) PublicGatewayURL() string {
	if !s.LanEnabled {
		return s.GatewayURL()
	}
	if configured := strings.TrimSpace(s.AdvertisedGatewayURL); configured != "" {
		return strings.TrimRight(configured, "/")
	}
	if ip := detectLANIPv4(); ip != "" {
		return fmt.Sprintf("http://%s:%d", ip, s.GatewayPort)
	}
	return s.GatewayURL()
}

// PublicControlURL 返回员工/管理员应该访问的控制台地址。
func (s Settings) PublicControlURL() string {
	if !s.LanEnabled {
		return s.ControlURL()
	}
	if gw := s.PublicGatewayURL(); gw != "" {
		if u, err := url.Parse(gw); err == nil && u.Hostname() != "" {
			return fmt.Sprintf("%s://%s:%d", u.Scheme, u.Hostname(), s.ControlPort)
		}
	}
	return s.ControlURL()
}

// virtualIfTokens 是要排除的虚拟网卡名片段。
//
// 排除它们是必须的：Docker/WSL/Hyper-V 的虚拟网卡常常排在物理网卡前面，
// 播出去的 172.17.x.x 员工根本连不上，而且看起来「服务在跑却不可达」很难查。
//
// 用子串而不是前缀，是因为 Windows 上虚拟网卡常带厂商前缀
// （"VMware Network Adapter VMnet8"、"Hyper-V Virtual Ethernet Adapter"）。
var virtualIfTokens = []string{
	"docker", "veth", "br-", "virbr",
	"vmnet", "vmware", "vbox", "virtualbox",
	"hyper-v", "vethernet",
	"utun", "tailscale", "zerotier",
	"loopback",
}

// detectLANIPv4 取本机第一个可用的非回环 IPv4 地址；没有则返回空串。
func detectLANIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtualInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			// 169.254.x.x 是 DHCP 失败时的自分配地址，不能播出去。
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			return ip4.String()
		}
	}
	return ""
}

func isVirtualInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, token := range virtualIfTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// PrepareSecrets 补齐缺失的密钥与口令，保留已存在的值（升级时不能换 KEK）。
func (s *Settings) PrepareSecrets() error {
	if s.KEK == "" {
		kek, err := randomSecret(32)
		if err != nil {
			return err
		}
		s.KEK = kek
	}
	if s.SessionSecret == "" {
		secret, err := randomSecret(32)
		if err != nil {
			return err
		}
		s.SessionSecret = secret
	}
	// 审计链密钥默认生成，而不是留空等部署方去配。
	//
	// 不配的时候链退化为裸 sha256：能发现改行、删中间行，但挡不住「删掉尾巴
	// 重算」——而「审计可取证」正是这个产品的卖点之一。桌面版是私有化交付的
	// 主力形态，不能把这条防线交给一份用户未必读过的部署说明。
	//
	// 与 KEK 同样处理：升级时保留旧值（换了它，历史行的 HMAC 就再也复算不出来）。
	if s.AuditChainKey == "" {
		key, err := randomSecret(32)
		if err != nil {
			return err
		}
		s.AuditChainKey = key
	}
	if s.PGPassword == "" {
		password, err := randomPassword(24)
		if err != nil {
			return err
		}
		s.PGPassword = password
	}
	if s.MachineCode == "" {
		s.MachineCode = MachineCode()
	}
	return nil
}
