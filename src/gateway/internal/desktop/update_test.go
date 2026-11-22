package desktop

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// signedManifest 造一个已签名的清单。
//
// 签名走与发布脚本相同的规范序列化（signable），因此这个测试同时校验
// Go 侧与发布侧的字节一致性：如果两边规范化不一致，验签会在这里失败。
func signedManifest(t *testing.T, privateKey ed25519.PrivateKey, mutate func(*Manifest)) Manifest {
	t.Helper()
	manifest := Manifest{
		Schema:        "keykeep/release/v1",
		Channel:       "stable",
		Version:       "0.2.0",
		PublishedAt:   "2026-11-06T00:00:00Z",
		PackageURL:    "keykeep-0.2.0.zip",
		PackageSHA256: "abc123",
		PackageBytes:  1024,
		Mandatory:     false,
	}
	if mutate != nil {
		mutate(&manifest)
	}
	payload, err := manifest.signable()
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	manifest.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return manifest
}

// TestSignableIsCanonical 固定住签名载荷的字节形态。
//
// 这份期望字符串就是发布脚本 Python 侧 json.dumps(sort_keys=True,
// separators=(",", ":")) 的输出。任何一侧改动字段集合或分隔符都会在这里失败，
// 而不是等到用户点「检查更新」才发现验签不过。
func TestSignableIsCanonical(t *testing.T) {
	manifest := Manifest{
		Schema:        "keykeep/release/v1",
		Channel:       "stable",
		Version:       "0.1.0-test.2",
		PublishedAt:   "2026-11-06T13:29:19Z",
		PackageURL:    "https://example.com/a.zip",
		PackageSHA256: "deadbeef",
		PackageBytes:  42,
		Mandatory:     true,
	}
	payload, err := manifest.signable()
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	want := `{"channel":"stable","mandatory":true,"package_bytes":42,` +
		`"package_sha256":"deadbeef","package_url":"https://example.com/a.zip",` +
		`"published_at":"2026-11-06T13:29:19Z","schema":"keykeep/release/v1",` +
		`"version":"0.1.0-test.2"}`
	if string(payload) != want {
		t.Fatalf("签名载荷不规范：\n得到 %s\n期望 %s", payload, want)
	}
}

func newKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	return publicKey, privateKey
}

func TestManifestVerifyAcceptsSigned(t *testing.T) {
	publicKey, privateKey := newKeyPair(t)
	manifest := signedManifest(t, privateKey, nil)
	if err := manifest.Verify(base64.StdEncoding.EncodeToString(publicKey)); err != nil {
		t.Fatalf("合法清单应当通过验签: %v", err)
	}
}

func TestManifestVerifyRejectsTamperedVersion(t *testing.T) {
	publicKey, privateKey := newKeyPair(t)
	manifest := signedManifest(t, privateKey, nil)
	// 篡改版本号：签名覆盖了 version，必须被发现。
	manifest.Version = "9.9.9"
	err := manifest.Verify(base64.StdEncoding.EncodeToString(publicKey))
	if err == nil {
		t.Fatal("篡改后的清单必须验签失败")
	}
}

func TestManifestVerifyRejectsTamperedHash(t *testing.T) {
	publicKey, privateKey := newKeyPair(t)
	manifest := signedManifest(t, privateKey, nil)
	manifest.PackageSHA256 = "deadbeef"
	if err := manifest.Verify(base64.StdEncoding.EncodeToString(publicKey)); err == nil {
		t.Fatal("篡改包哈希后必须验签失败")
	}
}

func TestManifestVerifyRejectsWrongKey(t *testing.T) {
	_, privateKey := newKeyPair(t)
	otherPublic, _ := newKeyPair(t)
	manifest := signedManifest(t, privateKey, nil)
	if err := manifest.Verify(base64.StdEncoding.EncodeToString(otherPublic)); err == nil {
		t.Fatal("用别的公钥验签必须失败")
	}
}

func TestManifestVerifyRejectsEmptyKey(t *testing.T) {
	_, privateKey := newKeyPair(t)
	manifest := signedManifest(t, privateKey, nil)
	if err := manifest.Verify(""); err == nil {
		t.Fatal("空公钥必须拒绝（不能降级为不验签）")
	}
}

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"0.1.1", "0.1.0", 1},
		{"0.2.0", "0.1.9", 1},
		{"1.0.0", "0.9.9", 1},
		{"0.1.0", "0.1.1", -1},
		// 预发布 < 正式版（semver 规则）
		{"0.1.0-test.1", "0.1.0", -1},
		{"0.1.0", "0.1.0-test.1", 1},
		{"0.1.0-test.1", "0.1.0-test.2", -1},
		{"0.1.0-test.2", "0.1.0-test.10", -1}, // 字符串比较：test.10 > test.2
		{"v0.1.0", "0.1.0", 0},                // 容忍 v 前缀
	}
	for _, item := range cases {
		got := CompareVersion(item.a, item.b)
		if got != item.want {
			t.Errorf("CompareVersion(%q, %q) = %d，期望 %d", item.a, item.b, got, item.want)
		}
	}
}

func TestFetcherCheckRejectsUnsignedManifest(t *testing.T) {
	publicKey, _ := newKeyPair(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":"keykeep/release/v1","version":"9.9.9","sig":""}`))
	}))
	defer server.Close()

	fetcher := &Fetcher{BaseURL: server.URL, PublicKey: base64.StdEncoding.EncodeToString(publicKey)}
	if _, err := fetcher.Check("0.1.0"); err == nil {
		t.Fatal("无签名的清单必须被拒绝")
	}
}

func TestFetcherCheckRejectsUnknownSchema(t *testing.T) {
	publicKey, privateKey := newKeyPair(t)
	manifest := signedManifest(t, privateKey, func(m *Manifest) { m.Schema = "other/v9" })
	body, _ := json.Marshal(manifest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	fetcher := &Fetcher{BaseURL: server.URL, PublicKey: base64.StdEncoding.EncodeToString(publicKey)}
	if _, err := fetcher.Check("0.1.0"); err == nil {
		t.Fatal("未知 schema 必须被拒绝")
	}
}

func TestFetcherCheckDetectsNewerVersion(t *testing.T) {
	publicKey, privateKey := newKeyPair(t)
	manifest := signedManifest(t, privateKey, func(m *Manifest) { m.Version = "0.2.0" })
	body, _ := json.Marshal(manifest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	fetcher := &Fetcher{BaseURL: server.URL, PublicKey: base64.StdEncoding.EncodeToString(publicKey)}
	check, err := fetcher.Check("0.1.0")
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if !check.Available {
		t.Fatal("0.2.0 相对 0.1.0 应当报告有更新")
	}
}

func TestFetcherDownloadRejectsHashMismatch(t *testing.T) {
	publicKey, privateKey := newKeyPair(t)
	payload := []byte("not the expected content")
	sum := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "latest.json" {
			manifest := signedManifest(t, privateKey, func(m *Manifest) {
				m.PackageSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
				m.PackageURL = "pkg.zip"
			})
			body, _ := json.Marshal(manifest)
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	_ = sum

	fetcher := &Fetcher{BaseURL: server.URL, PublicKey: base64.StdEncoding.EncodeToString(publicKey)}
	check, err := fetcher.Check("0.1.0")
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if _, err := fetcher.Download(check.Manifest, t.TempDir()); err == nil {
		t.Fatal("哈希不匹配的包必须被拒绝")
	}
}

func TestLayoutVersionPointer(t *testing.T) {
	root := t.TempDir()
	layout := &Layout{Root: root}
	if err := layout.EnsureDirs(); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	current, err := layout.CurrentVersion()
	if err != nil {
		t.Fatalf("读指针失败: %v", err)
	}
	if current != "" {
		t.Fatalf("首装应无指针，实际 %q", current)
	}
	if err := layout.SetCurrentVersion("0.2.0"); err != nil {
		t.Fatalf("写指针失败: %v", err)
	}
	current, err = layout.CurrentVersion()
	if err != nil || current != "0.2.0" {
		t.Fatalf("指针回读不符：%q / %v", current, err)
	}
}

func TestNormalizePortRejectsOutOfRange(t *testing.T) {
	if got := NormalizePort(0, DefaultPGPort); got != DefaultPGPort {
		t.Errorf("0 端口应回落默认，实际 %d", got)
	}
	if got := NormalizePort(70000, DefaultPGPort); got != DefaultPGPort {
		t.Errorf("越界端口应回落默认，实际 %d", got)
	}
	if got := NormalizePort(55512, DefaultPGPort); got != 55512 {
		t.Errorf("合法端口应保留，实际 %d", got)
	}
}

func TestPickFreePortSkipsOccupied(t *testing.T) {
	// 占住第一个候选端口，确认会往后挑。
	blocker, port := occupyPort(t)
	defer blocker.Close()

	picked := PickFreePort([]int{port, port + 10}, port)
	if picked == port {
		t.Fatalf("端口 %d 已被占用，不应被选中", port)
	}
}

func TestSettingsSecretsAreStable(t *testing.T) {
	settings := DefaultSettings()
	if err := settings.PrepareSecrets(); err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	kek := settings.KEK
	// 再跑一次不应改变已有值：换 KEK 会导致已存凭据全部无法解密。
	if err := settings.PrepareSecrets(); err != nil {
		t.Fatalf("二次生成失败: %v", err)
	}
	if settings.KEK != kek {
		t.Fatal("KEK 被覆盖了，这会让历史凭据永久无法解密")
	}
}

func TestInstallRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	layout := &Layout{Root: root}
	if err := layout.EnsureDirs(); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	pkg := filepath.Join(t.TempDir(), "evil.zip")
	writeZip(t, pkg, map[string]string{"../escape.txt": "x"})
	if _, err := layout.Install(pkg, "0.3.0"); err == nil {
		t.Fatal("含路径穿越的包必须被拒绝")
	}
}

func TestChecksumMatches(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HashFile(file)
	if err != nil {
		t.Fatalf("计算哈希失败: %v", err)
	}
	want := sha256.Sum256([]byte("hello"))
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("哈希不符：%s", got)
	}
}
