// Command fund-server：仙聊 Phase 1 独立资金服务（HTTP，监听回环地址）。
// 配置全部来自环境变量（生产由 systemd EnvironmentFile=/opt/xianliao/fund-service/fund.env 注入，文件 0600 root 专属）。
package main

import (
	"database/sql"
	"log"
	"net/http"

	_ "github.com/lib/pq"
	"xianliao-fund/internal/fund"
)

func main() {
	cfg, err := fund.ConfigFromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(20)
	if err := db.Ping(); err != nil {
		log.Fatalf("ping db: %v", err)
	}
	srv := fund.NewServer(cfg, db)
	log.Printf("fund-server listening on %s", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, srv.Handler()); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
