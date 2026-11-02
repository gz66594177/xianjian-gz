package fund

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
)

// ApplyMigration 在给定 DB 上执行一段 SQL（up 或 down）。迁移文件位于 migrations/，由 cmd/migrate 读取。
func ApplyMigration(db *sql.DB, sqlText string) error {
	_, err := db.Exec(sqlText)
	return err
}

// AppPasswordFromDSN 从 fund_app 的 DSN 中解析口令（仅 migrate 程序使用，口令不写日志、不进仓库）。
func AppPasswordFromDSN(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	if u.User == nil {
		return "", fmt.Errorf("DSN has no userinfo")
	}
	pw, ok := u.User.Password()
	if !ok {
		return "", fmt.Errorf("DSN has no password")
	}
	return pw, nil
}

func QuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
