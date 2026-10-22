package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	store, err := NewFileSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Load(); got.AutoStart {
		t.Fatal("缺少设置文件时应使用零值（不开机自启）")
	}
	if err := store.Save(Settings{AutoStart: true}); err != nil {
		t.Fatal(err)
	}
	// 新实例验证持久化，不用同一个对象的内存缓存。
	reloaded, err := NewFileSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Load(); !got.AutoStart {
		t.Fatalf("新实例应读回 AutoStart=true，实际 %+v", got)
	}
	if err := reloaded.Save(Settings{AutoStart: false}); err != nil {
		t.Fatal(err)
	}
	if got := store.Load(); got.AutoStart {
		t.Fatalf("关掉后应持久化 false，实际 %+v", got)
	}
}

func TestSettingsStoreCorruptFileFallsBackToZeroValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Load(); got.AutoStart {
		t.Fatalf("损坏设置应安全回退为 false，实际 %+v", got)
	}
}
