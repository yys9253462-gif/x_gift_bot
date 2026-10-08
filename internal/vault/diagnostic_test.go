package vault

import (
	"bytes"
	"fmt"
	"testing"
)

// testVault 复用 vault_test.go 里的 openNew。
func testVault(t *testing.T) *Vault {
	t.Helper()
	v, _ := openNew(t)
	return v
}

// queryNames 列出某前缀下的全部记录名，按写入顺序。
func queryNames(t *testing.T, v *Vault, prefix string) []string {
	t.Helper()
	rows, err := v.db.Query(
		"SELECT name FROM secrets WHERE name LIKE ? ESCAPE '!' ORDER BY rowid", likePrefix(prefix))
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("扫描失败：%v", err)
		}
		out = append(out, n)
	}
	return out
}

// 诊断记录必须有上限：凭据失效时重试循环每秒能写几条，永不清理的话
// vault 会一直涨，而诊断视图要一次性全量加载。
func TestPutDiagnosticIsBounded(t *testing.T) {
	v := testVault(t)
	const prefix = "x-read-failure:user:op"
	for i := 0; i < diagnosticKeepPerPrefix*3; i++ {
		if err := v.PutDiagnostic(prefix, []byte(fmt.Sprintf("record %d", i))); err != nil {
			t.Fatalf("写入第 %d 条失败：%v", i, err)
		}
	}
	n, err := v.CountDiagnostics(prefix)
	if err != nil {
		t.Fatalf("统计失败：%v", err)
	}
	if n > diagnosticKeepPerPrefix {
		t.Fatalf("诊断记录应被裁剪到 %d 条以内，实际 %d 条", diagnosticKeepPerPrefix, n)
	}
	if n < 2 {
		t.Fatalf("裁剪过头，只剩 %d 条，失去了排查价值", n)
	}
}

// 保留的必须是最新的那些：旧的证据先被裁掉。
func TestPutDiagnosticKeepsNewest(t *testing.T) {
	v := testVault(t)
	const prefix = "x-create-attempt:user"
	total := diagnosticKeepPerPrefix + 10
	for i := 0; i < total; i++ {
		if err := v.PutDiagnostic(prefix, []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("写入失败：%v", err)
		}
	}
	n, _ := v.CountDiagnostics(prefix)
	if n != diagnosticKeepPerPrefix {
		t.Fatalf("应恰好保留 %d 条，实际 %d", diagnosticKeepPerPrefix, n)
	}
	// 最新一条必须是最后写入的。
	rows := queryNames(t, v, prefix)
	found := false
	for _, name := range rows {
		raw, err := v.Get(name)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		if bytes.Contains(raw, []byte(fmt.Sprintf("v%d", total-1))) {
			found = true
		}
	}
	if !found {
		t.Fatal("最新一条记录被裁掉了，裁剪方向反了")
	}
}

// 裁剪绝不能碰到别的前缀：这是删除操作，误伤代价很高。
func TestPutDiagnosticDoesNotTouchOtherPrefixes(t *testing.T) {
	v := testVault(t)
	const mine = "x-read-failure:alice:op"
	const other = "x-read-failure:bob:op"
	const critical = "cookies"
	if err := v.Put("cookies", []byte(`{"secret":1}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < diagnosticKeepPerPrefix*2; i++ {
		if err := v.PutDiagnostic(mine, []byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := v.PutDiagnostic(other, []byte("y")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := v.Get("cookies"); err != nil {
		t.Fatalf("裁剪误删了 cookies：%v", err)
	}
	if n, _ := v.CountDiagnostics(other); n > diagnosticKeepPerPrefix {
		t.Fatalf("别人的前缀也被裁剪了：%d 条", n)
	}
}

// 前缀里含 LIKE 通配符时不能扩大匹配范围，否则会删到无关记录。
func TestLikePrefixEscapesWildcards(t *testing.T) {
	got := likePrefix("a%b_c")
	want := `a!%b!_c:%`
	if got != want {
		t.Fatalf("通配符未转义：得到 %q，应为 %q", got, want)
	}
}

// TrimDiagnostics 用于"同一条记录原地更新"的场景：写入本身不能裁剪，
// 必须在事件结束后单独调一次。
func TestTrimDiagnosticsKeepsFixedKeyUpdates(t *testing.T) {
	v := testVault(t)
	const prefix = "x-create-attempt:user"
	// 模拟 60 次下单，每次写一条（模拟一次尝试内的两次 persist 已合并）。
	for i := 0; i < 60; i++ {
		key := fmt.Sprintf("%s:%d", prefix, i)
		if err := v.Put(key, []byte(fmt.Sprintf("attempt %d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.TrimDiagnostics(prefix); err != nil {
		t.Fatalf("裁剪失败：%v", err)
	}
	n, _ := v.CountDiagnostics(prefix)
	if n != diagnosticKeepPerPrefix {
		t.Fatalf("裁剪后应剩 %d 条，实际 %d", diagnosticKeepPerPrefix, n)
	}
	// 最后写入的必须还在。
	if _, err := v.Get(fmt.Sprintf("%s:%d", prefix, 59)); err != nil {
		t.Fatalf("最新一条被裁掉了：%v", err)
	}
	// 最早那条应已被裁。
	if _, err := v.Get(fmt.Sprintf("%s:%d", prefix, 0)); err == nil {
		t.Fatal("最旧的记录未被裁掉")
	}
}
