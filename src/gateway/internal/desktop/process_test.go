package desktop

import "testing"

// Stop 的收尾顺序直接决定 PostgreSQL 能否干净关库（见 Stop 的注释）。
// 这里只钉住排序本身：数据库由 StopPostgres 单独收尾，不参与这个列表。
func TestSortForShutdownOrder(t *testing.T) {
	procs := []*Proc{
		{Name: "kkmock"},
		{Name: "control"},
		{Name: "gateway"},
	}

	sortForShutdown(procs)

	want := []string{"control", "gateway", "kkmock"}
	for i, name := range want {
		if procs[i].Name != name {
			t.Fatalf("第 %d 个应是 %s，实际 %s（整体: %v）", i, name, procs[i].Name, names(procs))
		}
	}
}

// 少于三个、或含未知名字时不应丢元素，也不应因为 map 里没有该键而恐慌。
func TestSortForShutdownHandlesSparseInput(t *testing.T) {
	cases := [][]string{
		{},
		{"control"},
		{"gateway", "control"},
		{"kkmock", "unknown"},
	}
	for _, c := range cases {
		procs := make([]*Proc, 0, len(c))
		for _, name := range c {
			procs = append(procs, &Proc{Name: name})
		}
		sortForShutdown(procs)
		if len(procs) != len(c) {
			t.Fatalf("排序后元素个数变了: %v", names(procs))
		}
	}
}

func names(procs []*Proc) []string {
	out := make([]string, 0, len(procs))
	for _, proc := range procs {
		out = append(out, proc.Name)
	}
	return out
}
