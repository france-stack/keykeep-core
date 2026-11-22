package shell

import (
	"errors"
	"testing"
)

// fakeRegistry 是 ValueReader 的注入式替身。
type fakeRegistry struct {
	values map[string]map[string]string // root -> path -> name -> value
	errs   map[string]error             // "root\\path" -> 读取错误
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{values: map[string]map[string]string{}, errs: map[string]error{}}
}

func (f *fakeRegistry) set(root, path, name, value string) {
	key := root + "\\" + path
	if f.values[key] == nil {
		f.values[key] = map[string]string{}
	}
	f.values[key][name] = value
}

func (f *fakeRegistry) fail(root, path string, err error) {
	f.errs[root+"\\"+path] = err
}

func (f *fakeRegistry) ReadString(root, path, name string) (string, bool, error) {
	key := root + "\\" + path
	if err, ok := f.errs[key]; ok {
		return "", false, err
	}
	values, ok := f.values[key]
	if !ok {
		return "", false, nil
	}
	value, ok := values[name]
	if !ok {
		return "", false, nil
	}
	return value, true, nil
}

func probePath(index int) (string, string) {
	probe := RuntimeProbes[index]
	return probe.Root, probe.Path
}

func TestDetectRuntimeFindsAnyLocation(t *testing.T) {
	for index := range RuntimeProbes {
		reader := newFakeRegistry()
		root, path := probePath(index)
		reader.set(root, path, "pv", "120.0.2210.91")
		version, err := DetectRuntime(reader)
		if err != nil {
			t.Fatalf("位置 %d 命中时不应报错: %v", index, err)
		}
		if version != "120.0.2210.91" {
			t.Fatalf("版本号 = %q，期望 120.0.2210.91", version)
		}
	}
}

func TestDetectRuntimeMissingWhenNoProbeHits(t *testing.T) {
	reader := newFakeRegistry()
	// 键存在但 pv 为空 / 0.0.0.0：卸载残留，不能当成可用。
	for index := range RuntimeProbes {
		root, path := probePath(index)
		reader.set(root, path, "pv", "")
	}
	if _, err := DetectRuntime(reader); !errors.Is(err, ErrRuntimeMissing) {
		t.Fatalf("空 pv 应当判定为未安装，实际 err=%v", err)
	}

	reader = newFakeRegistry()
	root, path := probePath(0)
	reader.set(root, path, "pv", " 0.0.0.0 ")
	if _, err := DetectRuntime(reader); !errors.Is(err, ErrRuntimeMissing) {
		t.Fatalf("0.0.0.0 应当判定为未安装，实际 err=%v", err)
	}
}

func TestDetectRuntimeSurvivesPartialReadFailures(t *testing.T) {
	reader := newFakeRegistry()
	// 第一处读取失败（企业策略锁 HKCU 的典型形态），第二处命中。
	root, path := probePath(0)
	reader.fail(root, path, errors.New("access denied"))
	root, path = probePath(1)
	reader.set(root, path, "pv", "121.0.2277.83")

	version, err := DetectRuntime(reader)
	if err != nil {
		t.Fatalf("单点读取失败不应影响结论: %v", err)
	}
	if version != "121.0.2277.83" {
		t.Fatalf("版本号 = %q", version)
	}
}

func TestDetectRuntimeReportsReadErrorsWhenNothingFound(t *testing.T) {
	reader := newFakeRegistry()
	cause := errors.New("access denied")
	for index := range RuntimeProbes {
		root, path := probePath(index)
		reader.fail(root, path, cause)
	}
	_, err := DetectRuntime(reader)
	if !errors.Is(err, ErrRuntimeMissing) {
		t.Fatalf("读取全失败时应带 ErrRuntimeMissing，实际 err=%v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("错误链里应保留原始读取错误，实际 err=%v", err)
	}
}

func TestDetectRuntimeRejectsNilReader(t *testing.T) {
	if _, err := DetectRuntime(nil); err == nil {
		t.Fatal("nil reader 应当返回错误")
	}
}

func TestRuntimeProbesCoverBothRoots(t *testing.T) {
	// ADR-013 §2.5 要求三处：HKLM 的 WOW6432Node、HKLM 原生、HKCU。
	seen := map[string]bool{}
	for _, probe := range RuntimeProbes {
		seen[probe.Root] = true
		if probe.Path == "" {
			t.Fatalf("%s 的探测路径为空", probe.Root)
		}
	}
	if !seen["HKLM"] || !seen["HKCU"] {
		t.Fatalf("探测位置必须同时覆盖 HKLM 与 HKCU，实际 %v", seen)
	}
	if len(RuntimeProbes) != 3 {
		t.Fatalf("探测位置应为 3 处，实际 %d", len(RuntimeProbes))
	}
}
