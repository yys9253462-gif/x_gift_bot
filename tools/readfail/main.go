// 只读：列出 vault 里的记录名（名字是明文），并可打印指定前缀记录的审计内容。
// 密文无法直接读，所以名字用 SQL 取，内容用 vault.Get 解密。
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"xgift/internal/vault"
)

func main() {
	db := "/var/lib/xgift/vault.db"
	if len(os.Args) > 1 {
		db = os.Args[1]
	}
	prefix := ""
	if len(os.Args) > 2 {
		prefix = os.Args[2]
	}

	// 记录名在明文表里，先直接列出来。
	raw, err := sql.Open("sqlite3", "file:"+db+"?mode=ro")
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		os.Exit(1)
	}
	defer raw.Close()
	rows, err := raw.Query("SELECT name FROM secrets ORDER BY name")
	if err != nil {
		fmt.Fprintf(os.Stderr, "查询记录名失败: %v\n", err)
		os.Exit(1)
	}
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	rows.Close()

	if prefix == "" {
		fmt.Printf("共 %d 条记录：\n", len(names))
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("  %s\n", n)
		}
		return
	}

	var hits []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			hits = append(hits, n)
		}
	}
	sort.Strings(hits)
	if len(hits) == 0 {
		fmt.Printf("没有以 %q 开头的记录\n", prefix)
		return
	}
	if len(hits) > 3 {
		hits = hits[len(hits)-3:]
	}

	v, err := vault.Open(db, os.Getenv("XGIFT_PASSWORD_FILE"), false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开保险库失败: %v\n", err)
		os.Exit(1)
	}
	defer v.Close()

	for _, n := range hits {
		body, err := v.Get(n)
		if err != nil {
			fmt.Printf("%s: 读取失败 %v\n", n, err)
			continue
		}
		var audit struct {
			Operation   string `json:"operation"`
			Phase       string `json:"phase"`
			HTTP        int    `json:"http_status"`
			Body        string `json:"body"`
			Cause       string `json:"cause"`
			Failure     string `json:"failure"`
			ContentType string `json:"content_type"`
			RequestID   string `json:"request_id"`
			EdgeID      string `json:"edge_id"`
			Variables   any    `json:"variables"`
		}
		if json.Unmarshal(body, &audit) != nil {
			fmt.Printf("=== %s ===\n  （非审计 JSON，前 300 字节）%s\n\n", n, trunc(string(body), 300))
			continue
		}
		fmt.Printf("=== %s ===\n", n)
		fmt.Printf("  操作         : %s\n", audit.Operation)
		fmt.Printf("  阶段         : %s\n", audit.Phase)
		fmt.Printf("  HTTP 状态    : %d\n", audit.HTTP)
		fmt.Printf("  变量         : %v\n", audit.Variables)
		fmt.Printf("  失败原因     : %s\n", audit.Failure)
		if audit.Cause != "" {
			fmt.Printf("  传输层原因   : %s\n", audit.Cause)
		}
		fmt.Printf("  Content-Type : %s\n", audit.ContentType)
		if audit.RequestID != "" {
			fmt.Printf("  X 请求 ID    : %s\n", audit.RequestID)
		}
		if audit.EdgeID != "" {
			fmt.Printf("  CF-Ray       : %s\n", audit.EdgeID)
		}
		fmt.Printf("  响应体       : %s\n\n", trunc(audit.Body, 1500))
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…（截断）"
}
