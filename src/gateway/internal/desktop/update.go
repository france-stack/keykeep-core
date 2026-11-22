package desktop

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Manifest 是发布清单 latest.json 的形状。
//
// 签名覆盖的是 canonicalManifestBytes(m)，不是整个文件：这样以后往 JSON 里
// 加字段（比如 changelog_zh）不会让旧版本的验签失效。
type Manifest struct {
	Schema        string   `json:"schema"`
	Channel       string   `json:"channel"`
	Version       string   `json:"version"`
	PublishedAt   string   `json:"published_at"`
	PackageURL    string   `json:"package_url"`
	PackageSHA256 string   `json:"package_sha256"`
	PackageBytes  int64    `json:"package_bytes"`
	Mandatory     bool     `json:"mandatory"`
	MinimumFrom   string   `json:"minimum_from"`
	Notes         []string `json:"notes"`
	Sig           string   `json:"sig"`
}

// signed 是参与签名的字段集合。
//
// 序列化必须与发布脚本（scripts/build/win-release.ps1 里内联的 Python 签名器）
// 逐字节一致，否则验签永远失败。约定：
//   - 键按字母序排列（Python 侧 sort_keys=True）；
//   - 分隔符无空格（Python 侧 separators=(",", ":")）。
//
// Go 的 encoding/json 按结构体声明顺序输出，所以字段声明顺序必须本身就是
// 字母序；并且字符串里的 <、>、& 会被转义成 \u003c 形式，与 Python 不同。
// 为避免这类差异，这里改成手写序列化，不经过 encoding/json。
type signed struct {
	Schema        string
	Channel       string
	Version       string
	PublishedAt   string
	PackageURL    string
	PackageSHA256 string
	PackageBytes  int64
	Mandatory     bool
}

func (m Manifest) signable() ([]byte, error) {
	// 键名按字母序：channel, mandatory, package_bytes, package_sha256,
	// package_url, published_at, schema, version。
	var builder strings.Builder
	builder.WriteString(`{"channel":`)
	writeJSONString(&builder, m.Channel)
	builder.WriteString(`,"mandatory":`)
	if m.Mandatory {
		builder.WriteString("true")
	} else {
		builder.WriteString("false")
	}
	builder.WriteString(`,"package_bytes":`)
	builder.WriteString(strconv.FormatInt(m.PackageBytes, 10))
	builder.WriteString(`,"package_sha256":`)
	writeJSONString(&builder, m.PackageSHA256)
	builder.WriteString(`,"package_url":`)
	writeJSONString(&builder, m.PackageURL)
	builder.WriteString(`,"published_at":`)
	writeJSONString(&builder, m.PublishedAt)
	builder.WriteString(`,"schema":`)
	writeJSONString(&builder, m.Schema)
	builder.WriteString(`,"version":`)
	writeJSONString(&builder, m.Version)
	builder.WriteString("}")
	return []byte(builder.String()), nil
}

// writeJSONString 按 JSON 规范转义字符串，但不做 HTML 转义
// （Python 的 json.dumps 默认不转义 <、>、&，Go 的 encoding/json 默认会）。
func writeJSONString(builder *strings.Builder, value string) {
	builder.WriteByte('"')
	for _, char := range value {
		switch char {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if char < 0x20 {
				builder.WriteString(fmt.Sprintf(`\u%04x`, char))
				continue
			}
			builder.WriteRune(char)
		}
	}
	builder.WriteByte('"')
}

// Verify 校验清单签名。公钥在构建期嵌入（publicKeyBase64）。
func (m Manifest) Verify(publicKeyBase64 string) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyBase64))
	if err != nil {
		return fmt.Errorf("内置公钥损坏: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return fmt.Errorf("内置公钥长度不对：%d", len(raw))
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(m.Sig))
	if err != nil {
		return fmt.Errorf("签名不是合法 base64: %w", err)
	}
	payload, err := m.signable()
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(raw), payload, sig) {
		return errors.New("清单签名校验失败：发布源可能被篡改")
	}
	return nil
}

// CompareVersion 比较语义化版本，返回 -1/0/1。
//
// 只处理本项目实际使用的形状：三段数字 + 可选预发布后缀（0.1.0-test.2）。
// 预发布排序遵循 semver：有后缀 < 无后缀；后缀按点分、逐段比较，
// 全数字段按数值比（test.2 < test.10），其余按字典序。
func CompareVersion(a, b string) int {
	aMain, aPre := splitVersion(a)
	bMain, bPre := splitVersion(b)
	for i := 0; i < 3; i++ {
		if aMain[i] != bMain[i] {
			if aMain[i] < bMain[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "":
		return 1 // 正式版 > 预发布
	case bPre == "":
		return -1
	}
	return comparePreRelease(aPre, bPre)
}

// comparePreRelease 逐段比较预发布标识。
func comparePreRelease(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	limit := len(aParts)
	if len(bParts) < limit {
		limit = len(bParts)
	}
	for i := 0; i < limit; i++ {
		aNum, aErr := strconv.Atoi(aParts[i])
		bNum, bErr := strconv.Atoi(bParts[i])
		switch {
		case aErr == nil && bErr == nil:
			if aNum != bNum {
				if aNum < bNum {
					return -1
				}
				return 1
			}
		case aErr == nil:
			return -1 // 数字段 < 字母段（semver 规则）
		case bErr == nil:
			return 1
		default:
			if aParts[i] != bParts[i] {
				if aParts[i] < bParts[i] {
					return -1
				}
				return 1
			}
		}
	}
	// 前缀相同：段数少的小（1.0.0-alpha < 1.0.0-alpha.1）。
	switch {
	case len(aParts) < len(bParts):
		return -1
	case len(aParts) > len(bParts):
		return 1
	}
	return 0
}

func splitVersion(v string) ([3]int, string) {
	v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
	main, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(main, ".")
	out := [3]int{}
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(strings.TrimSpace(parts[i]))
		out[i] = n
	}
	return out, pre
}

// UpdateCheck 是检查更新的结果。
type UpdateCheck struct {
	Available bool
	Manifest  Manifest
	Current   string
}

// Fetcher 负责从发布源拉清单与包。
type Fetcher struct {
	BaseURL string // 例：https://github.com/<owner>/<repo>/releases/latest/download
	Client  *http.Client
	// PublicKey 是 Ed25519 公钥（base64），构建期通过 -X 注入。
	PublicKey string
}

// ResolveBaseURL 决定实际使用的更新源。
//
// 优先级：环境变量 KEYKEEP_UPDATE_BASE > 构建期注入值。
// 允许环境变量覆盖，是为了让测试/离线部署能把更新源指到内网镜像，
// 而不必重新编译；生产默认仍用签名好的官方地址。
func ResolveBaseURL(buildValue string) string {
	if override := strings.TrimSpace(os.Getenv("KEYKEEP_UPDATE_BASE")); override != "" {
		return override
	}
	return buildValue
}

// Check 拉 latest.json 并判断是否有新版本。
func (f *Fetcher) Check(current string) (UpdateCheck, error) {
	url := strings.TrimRight(f.BaseURL, "/") + "/latest.json"
	raw, err := f.get(url, 2<<20)
	if err != nil {
		return UpdateCheck{}, fmt.Errorf("获取版本清单失败: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return UpdateCheck{}, fmt.Errorf("版本清单格式错误: %w", err)
	}
	if manifest.Schema != "keykeep/release/v1" {
		return UpdateCheck{}, fmt.Errorf("不认识的清单版本: %q", manifest.Schema)
	}
	if err := manifest.Verify(f.PublicKey); err != nil {
		return UpdateCheck{}, err
	}
	check := UpdateCheck{Manifest: manifest, Current: current}
	check.Available = CompareVersion(manifest.Version, current) > 0
	return check, nil
}

// Download 下载更新包并校验 sha256，返回本地临时文件路径。
func (f *Fetcher) Download(manifest Manifest, destDir string) (string, error) {
	if manifest.PackageBytes > 2<<30 {
		return "", fmt.Errorf("更新包体积异常：%d 字节", manifest.PackageBytes)
	}
	url, err := f.packageURL(manifest)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(destDir, "keykeep-"+manifest.Version+".zip")
	if err := f.downloadTo(url, target); err != nil {
		return "", err
	}
	sum, err := fileSHA256(target)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(sum, manifest.PackageSHA256) {
		os.Remove(target)
		return "", fmt.Errorf("更新包校验失败：期望 %s，实际 %s", manifest.PackageSHA256, sum)
	}
	return target, nil
}

// packageURL 决定从哪下载更新包。
//
// 规则：包文件名以清单里的 URL 为准，但主机优先用当前更新源。
// 这样同一份签名清单既能从 GitHub 拉、也能从内网镜像拉（KEYKEEP_UPDATE_BASE），
// 而 sha256 校验保证内容没被动过——换主机不会降级安全性。
func (f *Fetcher) packageURL(manifest Manifest) (string, error) {
	name := manifest.PackageURL
	if parsed, err := url.Parse(manifest.PackageURL); err == nil && parsed.Path != "" {
		name = path.Base(parsed.Path)
	}
	if name == "" || name == "." || name == "/" {
		return "", errors.New("清单里的包地址为空")
	}
	// 包名必须与版本对得上，避免清单与包被错配。
	if !strings.Contains(name, manifest.Version) {
		return "", fmt.Errorf("清单的包名 %q 与版本 %q 不匹配", name, manifest.Version)
	}
	base := strings.TrimRight(f.BaseURL, "/")
	if base == "" {
		return manifest.PackageURL, nil
	}
	return base + "/" + name, nil
}

func (f *Fetcher) get(url string, limit int64) ([]byte, error) {
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func (f *Fetcher) downloadTo(url, target string) error {
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Minute}
	}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载更新包失败：HTTP %d", resp.StatusCode)
	}
	tmp := target + ".part"
	file, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, target)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Install 把更新包解到 app/<version>/，返回新版本目录。
//
// 先解到 app/.staging-<version>/ 再整体改名：中途失败不会留下一个
// 「看起来能启动但缺文件」的版本目录，InstalledVersions 也不会认它。
//
// 更新包有两种形态，都要支持：
//  1. 版本目录内容本身（app\<版本>\ 这一层，含 kkdesk.exe）——最紧凑；
//  2. 完整便携包（根上有 KeyKeep.exe + app\<版本>\ + runtime\）——发布脚本
//     产出的就是这个，直接复用同一份 zip，不必为更新单做一种格式。
//
// 形态 2 还要把包里的 runtime\ 同步到安装根的 runtime\：Node 与 PostgreSQL
// 可能随版本升级（例如 Node 22 → 24），不同步会让新版本跑在旧运行时上。
// 同步被占用挡住时不失败，改存到版本目录里等下次启动换（见 ApplyPendingRuntime）。
func (l *Layout) Install(packagePath, version string) (string, error) {
	finalDir := l.VersionDir(version)
	if _, err := os.Stat(finalDir); err == nil {
		return finalDir, nil // 已装过同版本
	}
	deferRuntime := false
	if err := os.MkdirAll(l.AppDir(), 0o755); err != nil {
		return "", err
	}
	staging := filepath.Join(l.AppDir(), ".staging-"+version)
	_ = os.RemoveAll(staging)
	if err := unzipTo(packagePath, staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	// 更新包顶层可能带一层目录名，统一拍平到版本目录。
	if err := flattenSingleDir(staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}

	source := l.resolveVersionSource(staging, version, packagePath)
	if _, err := safeExe(source, "kkdesk.exe"); err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("更新包不完整: %w", err)
	}

	// 形态 2：先把 runtime 同步出去（此时 staging 里还有 runtime\ 目录）。
	//
	// runtime 未变化时跳过替换：控制台 node.exe 正从 runtime\ 里运行，
	// Windows 不允许重命名含打开文件的目录——文档宣称「不需要先关服务」，
	// 能兑现的前提就是 Node 版本没升级的常规更新必须在这里直接跳过。
	//
	// 指纹算不出来不能当成「没变化」放过：安装根缺 runtime（误删、旧包结构）时
	// 跳过会让这个安装永远修不好。replaceRuntime 的各条失败路径都保现场，
	// 所以「不确定有没有变」就替换是安全的一侧。
	runtimeSrc := filepath.Join(staging, "runtime")
	if isDir(runtimeSrc) {
		same, ferr := dirFingerprintEqual(runtimeSrc, l.RuntimeDir())
		switch {
		case ferr == nil && same:
			// 内容一致：删掉 staging 里的 runtime，避免它随版本目录占双份空间。
			_ = os.RemoveAll(runtimeSrc)
		default:
			err := l.replaceRuntime(runtimeSrc)
			switch {
			case err == nil:
				// 常规路径：运行时已经就地换好，staging 里的 runtime 由末尾统一清理。
				_ = os.RemoveAll(runtimeSrc)
			case errors.Is(err, ErrRuntimeInUse):
				// 占用中：不把更新判死。把包里的 runtime 暂存进版本目录，
				// 下次启动时（还没有任何进程从 runtime\ 里跑）再换。
				// 这是 A6 的根治办法——把危险动作挪到安全时刻，而不是在危险时刻硬试。
				deferRuntime = true
			default:
				_ = os.RemoveAll(staging)
				return "", fmt.Errorf("同步运行时失败: %w（数据与配置不受影响，现有 runtime 目录保持原样）", err)
			}
		}
	}

	// 把版本目录内容移到最终位置。
	if err := os.Rename(source, finalDir); err != nil {
		// 跨卷时 Rename 会失败，退化为复制。
		if copyErr := copyTree(source, finalDir); copyErr != nil {
			_ = os.RemoveAll(staging)
			return "", copyErr
		}
	}
	if deferRuntime && isDir(runtimeSrc) {
		// 只有「runtime 与版本目录平级」（完整便携包）时才需要再挪一次；
		// 版本目录内容直接打包的形态里，它已经随上面的改名落到 finalDir 了。
		// 同属 app\ 下的一次改名，不存在跨卷，失败就是目录冲突这类真异常，
		// 因此保留 staging 不删——那是新版运行时唯一还在的地方。
		pending := filepath.Join(finalDir, "runtime")
		if err := os.Rename(runtimeSrc, pending); err != nil {
			return "", fmt.Errorf("暂存待换运行时失败: %w（%s 仍在，新版运行时没丢；服务照旧用原 runtime）", err, staging)
		}
	}
	_ = os.RemoveAll(staging)
	return finalDir, nil
}

// ApplyPendingRuntime 在下一次启动换掉被占用没换成的运行时。
//
// 必须在启动任何子进程之前调用：这是整台机器上唯一一段「runtime\ 里没有正在
// 运行的 exe」的时间窗。换失败时现场不动，pending 目录继续留着，下次启动再试，
// 所以调用方只需要记日志，不需要中止启动。
func (l *Layout) ApplyPendingRuntime(versionDir string) (bool, error) {
	pending := filepath.Join(versionDir, "runtime")
	if !isDir(pending) {
		return false, nil
	}
	if same, err := dirFingerprintEqual(pending, l.RuntimeDir()); err == nil && same {
		_ = os.RemoveAll(pending)
		return false, nil
	}
	if err := l.replaceRuntime(pending); err != nil {
		return false, fmt.Errorf("应用待换运行时失败（runtime 保持原样，%s 里仍留着新版运行时）: %w", pending, err)
	}
	return true, nil
}

// resolveVersionSource 找出 staging 里「版本目录」的实际位置。
//
// 返回的目录必须直接包含 kkdesk.exe。识别顺序：
//   - staging/app/<version>/      （完整便携包）
//   - staging/<version>/          （带一层版本目录）
//   - staging/                    （版本目录内容）
func (l *Layout) resolveVersionSource(staging, version, packagePath string) string {
	candidates := []string{
		filepath.Join(staging, "app", version),
		filepath.Join(staging, version),
		staging,
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate, "kkdesk.exe")); err == nil {
			return candidate
		}
	}
	return staging
}

// dirFingerprintEqual 判断两个目录内容是否一致（相对路径 + 文件 SHA-256）。
// 用于更新时判断 runtime 是否真的变化；任一侧不存在返回 false。
func dirFingerprintEqual(a, b string) (bool, error) {
	fa, err := dirFingerprint(a)
	if err != nil {
		return false, err
	}
	fb, err := dirFingerprint(b)
	if err != nil {
		return false, err
	}
	return fa == fb, nil
}

// dirFingerprint 计算目录指纹：按相对路径排序，拼接每个文件的路径与内容哈希后整体 SHA-256。
func dirFingerprint(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s 不是目录", root)
	}
	type entry struct {
		rel  string
		hash string
	}
	var entries []entry
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return err
		}
		entries = append(entries, entry{rel: filepath.ToSlash(rel), hash: sum})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\x1f", e.rel, e.hash)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ErrRuntimeInUse 表示 runtime 目录里有正在运行的可执行文件，改名被系统拒绝。
// Install 据此把替换推迟到下次启动（ApplyPendingRuntime），而不是把更新判成失败：
// 唯一安全的替换窗口是子进程还没拉起来的那段启动早期，更新时点做不到。
var ErrRuntimeInUse = errors.New("运行时目录被占用")

// runtimeInUse 判定一次目录操作失败是不是「被占用」。
// Windows 的 ERROR_ACCESS_DENIED / ERROR_SHARING_VIOLATION / ERROR_LOCK_VIOLATION
// 在 Go 里都归到 os.ErrPermission；类 Unix 下 EACCES/EPERM 同理。
func runtimeInUse(err error) bool {
	return errors.Is(err, os.ErrPermission)
}

// dirFS 是替换运行时需要的最小文件系统动作。
//
// 抽成接口的原因只有一个：真机上「node.exe 正在跑」这个失败无法在测试里稳定复现，
// 而它恰恰是本函数最需要保证不出事的那条路径（A6）。注入之后，中断恢复、
// 占用重试、回滚失败三种现场都变成了可断言的普通用例。
type dirFS interface {
	Rename(oldPath, newPath string) error
	RemoveAll(path string) error
	IsDir(path string) bool
}

type osDirFS struct{}

func (osDirFS) Rename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }
func (osDirFS) RemoveAll(path string) error          { return os.RemoveAll(path) }
func (osDirFS) IsDir(path string) bool               { return isDir(path) }

// 进程退出后系统仍会短暂持有目录句柄，给几次短重试就能过去；
// 真正的「服务还在跑」则靠调用方停服务后重来。
const (
	runtimeRenameAttempts = 6
	runtimeRenameDelay    = 250 * time.Millisecond
)

// replaceRuntime 用更新包里的 runtime 覆盖安装根的 runtime。
//
// 顺序固定为「先恢复上次现场 → 再清历史备份 → 改名让位 → 就位 → 最后清理」，
// 任何一步失败都不会把机器上唯一可用的运行时删掉。
func (l *Layout) replaceRuntime(source string) error {
	return swapRuntime(l.RuntimeDir(), source, osDirFS{}, runtimeRenameAttempts, runtimeRenameDelay)
}

func swapRuntime(target, source string, fs dirFS, attempts int, delay time.Duration) error {
	backup := target + ".old"

	// 1) 上次中断的现场：runtime 没了但 runtime.old 还在——先放回去。
	//    这一步必须排在任何删除动作之前：此刻 runtime.old 是唯一能启动的运行时。
	if !fs.IsDir(target) && fs.IsDir(backup) {
		if err := fs.Rename(backup, target); err != nil {
			return fmt.Errorf("恢复上次未完成的运行时替换失败，可用的旧运行时仍在 %s: %w", backup, err)
		}
	}

	// 2) 只有确认 runtime 在位，才允许把历史备份当垃圾清掉。
	if err := fs.RemoveAll(backup); err != nil {
		return fmt.Errorf("清理旧运行时备份失败: %w", err)
	}

	// 3) 让位。
	if fs.IsDir(target) {
		if err := renameWithRetry(fs, target, backup, attempts, delay); err != nil {
			return err
		}
	}

	// 4) 新运行时就位；失败必须回滚，回滚也失败时把现场位置说清楚。
	if err := renameWithRetry(fs, source, target, attempts, delay); err != nil {
		if !fs.IsDir(backup) {
			// 第 3 步没让位（原本就没有 runtime 目录），旧运行时从没离开原位。
			return err
		}
		if rollbackErr := fs.Rename(backup, target); rollbackErr != nil {
			return fmt.Errorf("%v；且回滚失败——runtime 目录已不存在，可用的旧运行时在 %s（退出 KeyKeep 后手工改名回去）",
				err, backup)
		}
		// 回滚成功也要说清楚：否则用户只看到一句底层错误，无法判断机器现在能不能启动。
		return fmt.Errorf("%v；已回滚，现有 runtime 保持原样可继续启动", err)
	}

	// 5) 清理只在成功路径上做，失败留一手总比留一地好。
	_ = fs.RemoveAll(backup)
	return nil
}

// renameWithRetry 吞掉「进程刚退出、句柄未释放」的窗口期。
// 非占用类错误立刻返回——那种情况重试只是把失败拖成超时。
func renameWithRetry(fs dirFS, oldPath, newPath string, attempts int, delay time.Duration) error {
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(delay)
		}
		err := fs.Rename(oldPath, newPath)
		if err == nil {
			return nil
		}
		last = err
		if !runtimeInUse(err) {
			return err
		}
	}
	return fmt.Errorf("%w: %s 改名到 %s 失败，目录正被占用（最后错误: %v）", ErrRuntimeInUse, oldPath, newPath, last)
}

// isDir 判断路径是否为目录。
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// PruneVersions 保留当前版本与它的上一版，更老的删掉（回滚只需要上一版）。
func (l *Layout) PruneVersions(current string) error {
	l.pruneOldVersions(current)
	return nil
}

// pruneOldVersions 保留当前版本与上一版，更老的删掉（回滚只需要上一版）。
func (l *Layout) pruneOldVersions(current string) {
	versions, err := l.InstalledVersions()
	if err != nil {
		return
	}
	sort.Slice(versions, func(i, j int) bool { return CompareVersion(versions[i], versions[j]) < 0 })
	for _, version := range versions {
		if version == current {
			continue
		}
		// 保留比当前版本小的最近一个（回滚目标）。
		var rollback string
		for _, candidate := range versions {
			if CompareVersion(candidate, current) < 0 && candidate != current {
				rollback = candidate
			}
		}
		if version == rollback {
			continue
		}
		_ = os.RemoveAll(l.VersionDir(version))
	}
}

func unzipTo(archive, dest string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	base := filepath.Clean(dest)
	for _, entry := range reader.File {
		name := filepath.FromSlash(entry.Name)
		// 拒绝绝对路径与 ..：压缩包来自网络，必须当不可信输入处理。
		if filepath.IsAbs(name) || strings.Contains(name, "..") {
			return fmt.Errorf("更新包含非法路径: %s", entry.Name)
		}
		target := filepath.Join(base, name)
		if rel, err := filepath.Rel(base, target); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("更新包路径越界: %s", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := entry.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(src, 1<<30))
		src.Close()
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// flattenSingleDir 处理「压缩包顶层只有一个目录」的常见形态。
func flattenSingleDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	visible := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		visible = append(visible, entry)
	}
	if len(visible) != 1 || !visible[0].IsDir() {
		return nil
	}
	inner := filepath.Join(dir, visible[0].Name())
	children, err := os.ReadDir(inner)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := os.Rename(filepath.Join(inner, child.Name()), filepath.Join(dir, child.Name())); err != nil {
			return err
		}
	}
	return os.Remove(inner)
}

// ManifestJSON 序列化清单（发布脚本用）。
func ManifestJSON(m Manifest) ([]byte, error) {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// HashFile 供发布脚本计算包摘要。
func HashFile(path string) (string, error) { return fileSHA256(path) }

// SignaturePayload 返回应被签名的字节（发布脚本用）。
func (m Manifest) SignaturePayload() ([]byte, error) { return m.signable() }

// ZipEntryName 归一化 zip 内路径（测试与发布脚本共用）。
func ZipEntryName(name string) string { return path.Clean(strings.ReplaceAll(name, "\\", "/")) }

// bytesReader 便于测试构造内存包。
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
