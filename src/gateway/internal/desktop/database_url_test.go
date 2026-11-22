package desktop

import "testing"

// DatabaseUp 的 URL 选择是有代价的：DatabaseURL 带 Prisma 专有的 ?schema=public，
// pgx 不认，会把它当服务端启动参数发过去，PG 回 42704。用错那一个，
// DatabaseUp 恒为 false → 调用方以为库没在跑 → 既多拉一个 postgres.exe，
// 又在收尾时把正在服务的库停掉（备份一次，网关 503）。
//
// 这个测试钉住「两个 URL 的职责分工」，防止有人把它们合并成一个。
func TestSettingsDatabaseURLsAreDistinct(t *testing.T) {
	settings := Settings{PGPassword: "pw", PGPort: 55522}

	prisma := settings.DatabaseURL()
	gateway := settings.GatewayDatabaseURL()

	if prisma == gateway {
		t.Fatalf("两个连接串不该相同: %s", prisma)
	}
	if want := "?schema=public"; !hasSuffix(prisma, want) {
		t.Fatalf("DatabaseURL 是 Prisma 用，应带 %s，实际 %s", want, prisma)
	}
	if want := "?sslmode=disable"; !hasSuffix(gateway, want) {
		t.Fatalf("GatewayDatabaseURL 是 pgx 用，应带 %s，实际 %s", want, gateway)
	}
	// pgx 会解析不了的参数绝不能出现在给 pgx 的那一条里。
	if hasSubstring(gateway, "schema=") {
		t.Fatalf("GatewayDatabaseURL 给 pgx 用，不能带 schema 参数: %s", gateway)
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func hasSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
