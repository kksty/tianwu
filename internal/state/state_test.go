package state

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadReturnsTemplateWhenMissing(t *testing.T) {
	f := New(filepath.Join(t.TempDir(), "state.md"))
	got, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	// The template asks for durable facts and judgement, not a chronological
	// decision log: a "# 最近决策" section is what turned real notes into round
	// records (D106).
	for _, want := range []string{
		"# 已确认的事实与结论", "# 待验证线索", "# 已排除", "# 停放 / 阻塞",
		"# 环境 / 凭据 / 关键接口", "# 下一步",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("template missing %q", want)
		}
	}
	for _, unwanted := range []string{"最近决策", "当前假设"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("template still asks for %q: %q", unwanted, got)
		}
	}
}

func TestSaveThenLoad(t *testing.T) {
	f := New(filepath.Join(t.TempDir(), "sub", "state.md"))
	if err := f.Save("# 当前假设\n- 多租户隔离缺陷\n"); err != nil {
		t.Fatal(err)
	}
	got, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "多租户") {
		t.Fatalf("roundtrip lost content: %q", got)
	}
}

func TestAppendAndClear(t *testing.T) {
	f := New(filepath.Join(t.TempDir(), "state.md"))
	if err := f.Append("- 第一条"); err != nil {
		t.Fatal(err)
	}
	if err := f.Append("- 第二条\n"); err != nil {
		t.Fatal(err)
	}
	got, err := f.Current()
	if err != nil {
		t.Fatal(err)
	}
	if got != "- 第一条\n- 第二条\n" {
		t.Fatalf("append = %q", got)
	}
	if err := f.Clear(); err != nil {
		t.Fatal(err)
	}
	if got, err := f.Current(); err != nil || got != "" {
		t.Fatalf("clear = %q, %v", got, err)
	}
}

func TestAppendDoesNotCopyTheTemplate(t *testing.T) {
	f := New(filepath.Join(t.TempDir(), "state.md"))
	if err := f.Append("- only this"); err != nil {
		t.Fatal(err)
	}
	got, err := f.Current()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "# 当前假设") || !strings.Contains(got, "only this") {
		t.Fatalf("append started from template: %q", got)
	}
}

// The memory file has two writers (the decider's tool and PUT /api/state).
// Append is a read-modify-write: without a lock one paragraph disappears while
// both callers see success.
func TestConcurrentAppendLosesNothing(t *testing.T) {
	f := New(filepath.Join(t.TempDir(), "state.md"))
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := f.Append(fmt.Sprintf("- line %02d", i)); err != nil {
				t.Errorf("append: %v", err)
			}
		}(i)
	}
	wg.Wait()
	got, err := f.Current()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if !strings.Contains(got, fmt.Sprintf("- line %02d", i)) {
			t.Fatalf("append %02d lost: %q", i, got)
		}
	}
}
