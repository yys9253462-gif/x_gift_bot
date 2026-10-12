package site

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

// 前台首页只提供「检测赠送资格」。它不是一句文案约定，而是产品边界：
// 兑换、进度查询、付款链接生成都不该从公开页面露出入口。
// 这里同时校验 HTML 与打包后的 app.js，防止以后新增组件时把功能悄悄带回来。
func TestFrontPageExposesOnlyEligibility(t *testing.T) {
	html := readAsset(t, "index.html")
	for _, want := range []string{"检测赠送资格", "app.js"} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html 缺少 %q", want)
		}
	}
	for _, gone := range []string{"Premium 兑换</title>", "使用兑换码，为你的 X 账号兑换 Premium"} {
		if strings.Contains(html, gone) {
			t.Errorf("index.html 仍保留已移除的文案 %q", gone)
		}
	}

	// app.js 里非 ASCII 被压成 \uXXXX，先还原再比对中文文案。
	script := decodeEscapes(t, readAsset(t, "app.js"))
	for _, want := range []string{"检测赠送资格", "开始检测", "X 用户名"} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js 缺少 %q", want)
		}
	}
	// 兑换码形如 XG-<48 hex>；前台不再校验输入，正则不应被带进包里。
	for _, gone := range []string{"兑换 Premium", "查询兑换进度", "Premium 兑换", "XG-"} {
		if strings.Contains(script, gone) {
			t.Errorf("app.js 仍包含已移除的功能 %q", gone)
		}
	}
}

// readAsset 读取内嵌的前台资源原始字节。
func readAsset(t *testing.T, name string) string {
	t.Helper()
	raw, err := assets.ReadFile("assets/" + name)
	if err != nil {
		t.Fatalf("读取内嵌资源 %s: %v", name, err)
	}
	return string(raw)
}

// decodeEscapes 还原 esbuild 对非 ASCII 输出的 \uXXXX 转义。
func decodeEscapes(t *testing.T, s string) string {
	t.Helper()
	var out strings.Builder
	for i := 0; i < len(s); {
		if i+6 <= len(s) && s[i] == '\\' && s[i+1] == 'u' {
			if v, ok := hex4(s[i+2 : i+6]); ok {
				out.WriteRune(v)
				i += 6
				continue
			}
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

func hex4(s string) (rune, bool) {
	var v rune
	for _, c := range []byte(s) {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			v |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= rune(c-'A') + 10
		default:
			return 0, false
		}
	}
	return v, true
}

// 资产以 gzip 形式内嵌时也必须能解出同样内容，避免预压缩产物与源文件脱节。
func TestFrontPageGzipMatchesSource(t *testing.T) {
	plain := readAsset(t, "app.js")
	compressed, err := assets.ReadFile("assets/app.js.gz")
	if err != nil {
		t.Fatalf("读取 app.js.gz: %v", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("解压 app.js.gz: %v", err)
	}
	defer zr.Close()
	restored, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("读取 app.js.gz: %v", err)
	}
	if string(restored) != plain {
		t.Fatal("app.js.gz 与 app.js 内容不一致，疑似只重建了其中一个")
	}
}
