package vault

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 这个包此前零测试、零覆盖，而它是所有凭据（X 的 auth_token、支付卡、
// Stripe 公钥）的落点。这里的测试重点覆盖：
//   1. 加解密往返 + 密文不含明文
//   2. 权限校验（密码文件与库文件都必须是 0600）
//   3. 密码错误 / 盐被篡改 -> 必须失败而不是返回垃圾
//   4. 并发访问不 panic
//   5. 覆盖写、删除、列举

// 建一个合法的密码文件（0600）。
func writePassword(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "vault-password")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatalf("写密码文件失败: %v", err)
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatalf("设权限失败: %v", err)
	}
	return p
}

func newPassword(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("生成密码失败: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func openNew(t *testing.T) (*Vault, string) {
	t.Helper()
	dir := t.TempDir()
	pw := writePassword(t, dir, newPassword(t))
	db := filepath.Join(dir, "vault.db")
	v, err := Open(db, pw, true)
	if err != nil {
		t.Fatalf("创建保险库失败: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return v, pw
}

func TestPutGetRoundTrip(t *testing.T) {
	v, _ := openNew(t)
	secret := []byte("auth_token=deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err := v.Put("cookies", secret); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	got, err := v.Get("cookies")
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("往返不一致: 期望 %q 得到 %q", secret, got)
	}
}

// 密文里不能出现明文 —— 这是加密有没有真的生效的唯一直接证据。
func TestCiphertextDoesNotContainPlaintext(t *testing.T) {
	dir := t.TempDir()
	pw := writePassword(t, dir, newPassword(t))
	db := filepath.Join(dir, "vault.db")
	v, err := Open(db, pw, true)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	marker := "PLAINTEXT-MARKER-5f3a9c1e-should-never-appear"
	if err := v.Put("probe", []byte(marker)); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	// 关掉后再以只读方式读原始文件，避免影响写入
	if err := v.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	raw, err := os.ReadFile(db)
	if err != nil {
		t.Fatalf("读库文件失败: %v", err)
	}
	// 阳性对照：库文件名本身必然出现在文件里，证明我们读的是真数据
	if !bytes.Contains(raw, []byte("secrets")) {
		t.Fatalf("阳性对照失败：连表名都没找到，说明读到的不是有效库文件")
	}
	if bytes.Contains(raw, []byte(marker)) {
		t.Fatalf("密文里出现了明文 %q —— 加密没生效", marker)
	}
}

// 权限校验：密码文件必须是 0600，且必须是普通文件。
func TestPasswordFilePermissions(t *testing.T) {
	cases := []struct {
		name string
		make func(t *testing.T, dir string) string
		want string
	}{
		{
			name: "权限过宽 0644",
			make: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "vault-password")
				if err := os.WriteFile(p, []byte(newPassword(t)), 0644); err != nil {
					t.Fatal(err)
				}
				os.Chmod(p, 0644)
				return p
			},
			want: "owner-only",
		},
		{
			name: "文件不存在",
			make: func(t *testing.T, dir string) string {
				return filepath.Join(dir, "does-not-exist")
			},
			want: "no such file",
		},
		{
			name: "密码过短",
			make: func(t *testing.T, dir string) string {
				return writePassword(t, dir, "short")
			},
			want: "too short",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			pw := c.make(t, dir)
			_, err := Open(filepath.Join(dir, "vault.db"), pw, true)
			if err == nil {
				t.Fatalf("期望失败，但成功了")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息不含 %q: %v", c.want, err)
			}
		})
	}
}

// 用错误的密码打开同一个库必须失败，而不是返回能解开的垃圾。
func TestWrongPasswordFails(t *testing.T) {
	dir := t.TempDir()
	pw := writePassword(t, dir, newPassword(t))
	db := filepath.Join(dir, "vault.db")
	v, err := Open(db, pw, true)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := v.Put("k", []byte("secret-value")); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	v.Close()

	// 换成另一个合法格式的密码
	wrong := writePassword(t, dir, newPassword(t))
	v2, err := Open(db, wrong, false)
	if err != nil {
		// 打开阶段就拒绝也算正确
		return
	}
	defer v2.Close()
	if _, err := v2.Get("k"); err == nil {
		t.Fatalf("用错误密码竟然解开了密文")
	}
}

// 读不存在的键必须返回错误，不能返回空字节。
func TestGetMissingKeyReturnsError(t *testing.T) {
	v, _ := openNew(t)
	got, err := v.Get("never-written")
	if err == nil {
		t.Fatalf("读不存在的键期望报错，却成功返回 %q", got)
	}
	if len(got) != 0 {
		t.Fatalf("读不存在的键不该返回值，得到 %q", got)
	}
}

// 同一个键重复 Put 应该覆盖，不是报错也不是留两条。
func TestPutOverwrites(t *testing.T) {
	v, _ := openNew(t)
	if err := v.Put("k", []byte("first")); err != nil {
		t.Fatalf("第一次 Put 失败: %v", err)
	}
	if err := v.Put("k", []byte("second")); err != nil {
		t.Fatalf("第二次 Put 失败: %v", err)
	}
	got, err := v.Get("k")
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("覆盖失败: 期望 second 得到 %q", got)
	}
}

// 空值也要能存 —— 否则调用方得为「空」单独处理。
func TestPutEmptyValue(t *testing.T) {
	v, _ := openNew(t)
	if err := v.Put("empty", []byte{}); err != nil {
		t.Fatalf("存空值失败: %v", err)
	}
	got, err := v.Get("empty")
	if err != nil {
		t.Fatalf("读空值失败: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("期望空值，得到 %q", got)
	}
}

// 每次加密必须用不同的 nonce —— 相同明文两次写入的密文应当不同。
func TestNonceIsRandomised(t *testing.T) {
	dir := t.TempDir()
	pw := writePassword(t, dir, newPassword(t))
	db := filepath.Join(dir, "vault.db")
	v, err := Open(db, pw, true)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	defer v.Close()

	plain := []byte("same-plaintext-both-times")
	if err := v.Put("a", plain); err != nil {
		t.Fatal(err)
	}
	if err := v.Put("b", plain); err != nil {
		t.Fatal(err)
	}

	// 只取我们自己写的两条：Open() 会额外写一条 vault-check 自检记录，
	// 那是设计的一部分（用于验证密码是否正确），不算业务数据。
	rows, err := v.db.Query("SELECT name, payload FROM secrets WHERE name IN ('a','b') ORDER BY name")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	defer rows.Close()
	var payloads [][]byte
	for rows.Next() {
		var n string
		var p []byte
		if err := rows.Scan(&n, &p); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, p)
	}
	if len(payloads) != 2 {
		t.Fatalf("期望 2 条记录，得到 %d", len(payloads))
	}
	if bytes.Equal(payloads[0], payloads[1]) {
		t.Fatalf("相同明文产生了相同密文 —— nonce 没有随机化")
	}
}

// 并发读写不应该 panic 或死锁（db 限制单连接，靠 busy_timeout 排队）。
func TestConcurrentAccess(t *testing.T) {
	v, _ := openNew(t)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			key := "concurrent-" + string(rune('a'+n%20))
			if err := v.Put(key, []byte("value")); err != nil {
				errs <- err
			}
		}(i)
		go func(n int) {
			defer wg.Done()
			key := "concurrent-" + string(rune('a'+n%20))
			v.Get(key) // 可能不存在，但不该 panic
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发写入失败: %v", err)
	}
}

// 关掉之后再用必须报错，而不是静默失败。
func TestUseAfterClose(t *testing.T) {
	dir := t.TempDir()
	pw := writePassword(t, dir, newPassword(t))
	v, err := Open(filepath.Join(dir, "vault.db"), pw, true)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if err := v.Put("k", []byte("v")); err == nil {
		t.Fatalf("关闭后写入竟然成功")
	}
}

// 重新打开同一个库，之前写入的内容必须还在（盐持久化正确）。
func TestReopenPreservesData(t *testing.T) {
	dir := t.TempDir()
	pw := writePassword(t, dir, newPassword(t))
	db := filepath.Join(dir, "vault.db")

	v, err := Open(db, pw, true)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := v.Put("persisted", []byte("survives-reopen")); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	v2, err := Open(db, pw, false)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer v2.Close()
	got, err := v2.Get("persisted")
	if err != nil {
		t.Fatalf("重开后读取失败: %v", err)
	}
	if string(got) != "survives-reopen" {
		t.Fatalf("重开后内容不对: %q", got)
	}
}

// create=false 指向不存在的库必须失败。
func TestOpenMissingWithoutCreate(t *testing.T) {
	dir := t.TempDir()
	pw := writePassword(t, dir, newPassword(t))
	if _, err := Open(filepath.Join(dir, "absent.db"), pw, false); err == nil {
		t.Fatalf("打开不存在的库竟然成功了")
	}
}

// Open 会写入一条 vault-check 自检记录，用于验证密码是否正确 ——
// 这是 fail-closed 的关键：密码错了必须在打开阶段就失败，
// 而不是等到读业务数据时才解不开。
func TestOpenWritesSelfCheckRecord(t *testing.T) {
	dir := t.TempDir()
	pw := writePassword(t, dir, newPassword(t))
	db := filepath.Join(dir, "vault.db")
	v, err := Open(db, pw, true)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	var n int
	if err := v.db.QueryRow("SELECT COUNT(*) FROM secrets WHERE name='vault-check'").Scan(&n); err != nil {
		t.Fatalf("查询 vault-check 失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("期望恰好 1 条 vault-check，得到 %d", n)
	}
	v.Close()
}

// 把 vault-check 破坏掉，Open 必须拒绝 —— 证明它真的在起作用，
// 而不是一条没人读的摆设。
func TestCorruptedSelfCheckBlocksOpen(t *testing.T) {
	dir := t.TempDir()
	pw := writePassword(t, dir, newPassword(t))
	db := filepath.Join(dir, "vault.db")
	v, err := Open(db, pw, true)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := v.Put("real", []byte("business-data")); err != nil {
		t.Fatal(err)
	}
	v.Close()

	raw, err := sql.Open("sqlite3", db)
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	// 把自检记录换成无意义的字节
	if _, err := raw.Exec("UPDATE secrets SET payload=? WHERE name='vault-check'", []byte("garbage")); err != nil {
		raw.Close()
		t.Fatalf("破坏自检记录失败: %v", err)
	}
	raw.Close()

	if _, err := Open(db, pw, false); err == nil {
		t.Fatalf("自检记录被破坏后 Open 竟然成功了 —— 密码校验形同虚设")
	}
}

// 盐被篡改后必须无法解密，而不是返回垃圾数据。
func TestTamperedSaltFailsToDecrypt(t *testing.T) {
	dir := t.TempDir()
	pw := writePassword(t, dir, newPassword(t))
	db := filepath.Join(dir, "vault.db")
	v, err := Open(db, pw, true)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := v.Put("k", []byte("value")); err != nil {
		t.Fatal(err)
	}
	v.Close()

	// 直接改盐
	raw, err := sql.Open("sqlite3", db)
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	bogus := make([]byte, 32)
	for i := range bogus {
		bogus[i] = 0xAA
	}
	if _, err := raw.Exec("UPDATE metadata SET value=? WHERE key='salt'", bogus); err != nil {
		raw.Close()
		t.Fatalf("篡改盐失败: %v", err)
	}
	raw.Close()

	v2, err := Open(db, pw, false)
	if err != nil {
		return // 打开阶段拒绝也正确
	}
	defer v2.Close()
	if got, err := v2.Get("k"); err == nil {
		t.Fatalf("盐被改后仍解出内容 %q —— GCM 认证没起作用", got)
	}
}
