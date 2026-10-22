package main

import (
	"errors"
	"testing"

	"meccyradar/internal/config"
)

// fakeAutostart 是 autostartManager 的内存实现，用来在任意平台上验证对齐规则。
type fakeAutostart struct {
	exists   bool
	target   string
	failOn   string
	created  []string
	removals int
}

func (f *fakeAutostart) ShortcutExists() (bool, error) {
	if f.failOn == "exists" {
		return false, errors.New("exists failed")
	}
	return f.exists, nil
}

func (f *fakeAutostart) ShortcutTarget() (string, error) {
	if f.failOn == "target" {
		return "", errors.New("target failed")
	}
	return f.target, nil
}

func (f *fakeAutostart) CreateShortcut(exePath string) error {
	if f.failOn == "create" {
		return errors.New("create failed")
	}
	f.created = append(f.created, exePath)
	f.exists = true
	f.target = exePath
	return nil
}

func (f *fakeAutostart) RemoveShortcut() error {
	if f.failOn == "remove" {
		return errors.New("remove failed")
	}
	f.removals++
	f.exists = false
	f.target = ""
	return nil
}

func TestReconcileCreatesMissingShortcut(t *testing.T) {
	manager := &fakeAutostart{exists: false}
	outcome := reconcileAutostart(config.Settings{AutoStart: true}, manager, `C:\Apps\meccyradar.exe`)

	if !outcome.Created || outcome.Error != "" {
		t.Fatalf("设置开启且快捷方式缺失时应重建，实际 %+v", outcome)
	}
	if len(manager.created) != 1 || manager.created[0] != `C:\Apps\meccyradar.exe` {
		t.Fatalf("重建路径不对：%v", manager.created)
	}
}

func TestReconcileRemovesStaleShortcut(t *testing.T) {
	manager := &fakeAutostart{exists: true, target: `C:\Apps\meccyradar.exe`}
	outcome := reconcileAutostart(config.Settings{AutoStart: false}, manager, `C:\Apps\meccyradar.exe`)

	if !outcome.Removed || outcome.Error != "" {
		t.Fatalf("设置关闭但快捷方式残留时应删除，实际 %+v", outcome)
	}
	if manager.removals != 1 {
		t.Fatalf("应删除一次，实际 %d", manager.removals)
	}
}

func TestReconcileUpdatesChangedExecutable(t *testing.T) {
	manager := &fakeAutostart{exists: true, target: `C:\Old\meccyradar.exe`}
	outcome := reconcileAutostart(config.Settings{AutoStart: true}, manager, `C:\New\meccyradar.exe`)

	if !outcome.Updated || outcome.Error != "" {
		t.Fatalf("exe 路径变了应更新，实际 %+v", outcome)
	}
	if manager.target != `C:\New\meccyradar.exe` {
		t.Fatalf("更新后应指向新路径，实际 %q", manager.target)
	}
}

func TestReconcileNoopWhenConsistent(t *testing.T) {
	cases := []struct {
		name     string
		settings config.Settings
		manager  *fakeAutostart
	}{
		{"开启且指向正确", config.Settings{AutoStart: true}, &fakeAutostart{exists: true, target: `C:\Apps\meccyradar.exe`}},
		{"关闭且无快捷方式", config.Settings{AutoStart: false}, &fakeAutostart{exists: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := reconcileAutostart(tc.settings, tc.manager, `C:\Apps\meccyradar.exe`)
			if outcome.Created || outcome.Removed || outcome.Updated || outcome.Error != "" {
				t.Fatalf("状态一致时不该做任何事，实际 %+v", outcome)
			}
			if len(tc.manager.created) != 0 || tc.manager.removals != 0 {
				t.Fatalf("不该有副作用：created=%v removals=%d", tc.manager.created, tc.manager.removals)
			}
		})
	}
}

// 快捷方式里可能带引号、大小写不同、用正斜杠 —— 都要认成同一个路径，
// 否则每次启动都会以为"路径变了"重写一遍。
func TestReconcileTreatsEquivalentPathsAsSame(t *testing.T) {
	manager := &fakeAutostart{exists: true, target: `"C:/Apps/MeccyRadar.exe"`}
	outcome := reconcileAutostart(config.Settings{AutoStart: true}, manager, `c:\apps\meccyradar.exe`)

	if outcome.Updated || outcome.Error != "" {
		t.Fatalf("等价路径不该触发更新，实际 %+v", outcome)
	}
	if len(manager.created) != 0 {
		t.Fatalf("不该重写快捷方式：%v", manager.created)
	}
}

// 失败不能 panic，也不能假装成功：错误要能回传，让上层决定是否回滚设置。
func TestReconcileSurfacesErrors(t *testing.T) {
	for _, failOn := range []string{"exists", "target", "create", "remove"} {
		manager := &fakeAutostart{exists: true, target: `C:\Old\meccyradar.exe`, failOn: failOn}
		outcome := reconcileAutostart(config.Settings{AutoStart: true}, manager, `C:\New\meccyradar.exe`)
		if failOn == "remove" {
			// remove 只在"设置关闭"时才走，这里设置是开启的，不会触发。
			continue
		}
		if outcome.Error == "" {
			t.Fatalf("%s 失败时必须有错误，实际 %+v", failOn, outcome)
		}
	}

	manager := &fakeAutostart{exists: true, failOn: "remove"}
	outcome := reconcileAutostart(config.Settings{AutoStart: false}, manager, `C:\Apps\meccyradar.exe`)
	if outcome.Error == "" {
		t.Fatal("删除失败时必须有错误")
	}
}

func TestSamePath(t *testing.T) {
	same := [][2]string{
		{`C:\Apps\meccyradar.exe`, `c:\apps\meccyradar.exe`},
		{`"C:\Apps\meccyradar.exe"`, `C:\Apps\meccyradar.exe`},
		{`C:/Apps/meccyradar.exe`, `C:\Apps\meccyradar.exe`},
	}
	for _, pair := range same {
		if !samePath(pair[0], pair[1]) {
			t.Fatalf("应视为同一路径：%q vs %q", pair[0], pair[1])
		}
	}
	different := [][2]string{
		{`C:\Apps\meccyradar.exe`, `C:\Other\meccyradar.exe`},
		{`C:\Apps\meccyradar.exe`, ``},
		{``, ``},
	}
	for _, pair := range different {
		if samePath(pair[0], pair[1]) {
			t.Fatalf("不该视为同一路径：%q vs %q", pair[0], pair[1])
		}
	}
}

func TestHasAutostartFlag(t *testing.T) {
	if !hasAutostartFlag([]string{"--autostart"}) {
		t.Fatal("应识别 --autostart")
	}
	if !hasAutostartFlag([]string{"foo", "--autostart", "bar"}) {
		t.Fatal("参数在中间也要识别")
	}
	if hasAutostartFlag(nil) || hasAutostartFlag([]string{"--other"}) {
		t.Fatal("不该误判")
	}
}
