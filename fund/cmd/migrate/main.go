// Command migrate：资金库版本化迁移工具（须以管理员账号执行，只动资金库）。
// 用法：migrate up|down
// 环境变量：FUND_ADMIN_DATABASE_URL（管理员 DSN）、FUND_DATABASE_URL（业务 DSN，仅用于解析 fund_app 口令并设置角色口令）、
// MIGRATIONS_DIR（默认 ./migrations）。
package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "github.com/lib/pq"
	"xianliao-fund/internal/fund"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "down") {
		fmt.Fprintln(os.Stderr, "usage: migrate up|down")
		os.Exit(2)
	}
	dir := os.Getenv("MIGRATIONS_DIR")
	if dir == "" {
		dir = "./migrations"
	}
	adminDSN := os.Getenv("FUND_ADMIN_DATABASE_URL")
	if adminDSN == "" {
		log.Fatal("FUND_ADMIN_DATABASE_URL is empty")
	}
	// 按版本号顺序执行：up 为 0001 -> ... -> 0010；down 为逆序回滚。
	var files []string
	if os.Args[1] == "up" {
		files = []string{"0001_init.up.sql", "0002_recovery.up.sql", "0003_redpacket.up.sql", "0004_group_redpacket.up.sql", "0005_group_lucky_redpacket.up.sql", "0006_transfer.up.sql", "0007_admin.up.sql", "0008_recharge.up.sql", "0009_withdraw.up.sql", "0010_manual_gateway.up.sql", "0011_withdraw_qr.up.sql", "0012_gateway_channels.up.sql", "0013_balance_adjust.up.sql", "0014_phone_and_name.up.sql", "0015_tx_password_admin_reset.up.sql"}
	} else {
		files = []string{"0015_tx_password_admin_reset.down.sql", "0014_phone_and_name.down.sql", "0013_balance_adjust.down.sql", "0012_gateway_channels.down.sql", "0011_withdraw_qr.down.sql", "0010_manual_gateway.down.sql", "0009_withdraw.down.sql", "0008_recharge.down.sql", "0007_admin.down.sql", "0006_transfer.down.sql", "0005_group_lucky_redpacket.down.sql", "0004_group_lucky_redpacket.down.sql", "0003_redpacket.down.sql", "0002_recovery.down.sql", "0001_init.down.sql"}
	}
	db, err := sql.Open("postgres", adminDSN)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	for _, file := range files {
		sqlBytes, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			log.Fatalf("read migration: %v", err)
		}
		if err := fund.ApplyMigration(db, string(sqlBytes)); err != nil {
			log.Fatalf("apply %s: %v", file, err)
		}
		log.Printf("migration %s applied", file)
	}
	if os.Args[1] == "up" {
		// 设置 fund_app 角色口令（口令来自业务 DSN，不打印）。
		appDSN := os.Getenv("FUND_DATABASE_URL")
		if appDSN == "" {
			log.Fatal("FUND_DATABASE_URL is empty (needed to set fund_app password)")
		}
		pw, err := fund.AppPasswordFromDSN(appDSN)
		if err != nil {
			log.Fatalf("parse app password: %v", err)
		}
		if _, err := db.Exec(`ALTER ROLE fund_app WITH PASSWORD ` + fund.QuoteLiteral(pw)); err != nil {
			log.Fatalf("set fund_app password: %v", err)
		}
	}
	log.Printf("migration %s complete", os.Args[1])
}
