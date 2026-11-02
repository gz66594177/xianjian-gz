package fund

// Phase 1 15 项真实自动化测试（go test）。
// 测试库：同一 Postgres 上的 xianliao_fund_test（由 TestMain 在 maintenance 库上建库并跑迁移 up）。
// 环境变量：FUND_TEST_ADMIN_URL（指向 xianliao_fund_test 的管理员 DSN；TestMain 会把库名换成 postgres 建库）。
// 跑前跑后清理测试数据（TestMain 收尾 TRUNCATE，平台户除外逻辑：truncate 后重插平台户）。
// 注意：测试用 HTTP 服务的 Redis 地址取自 FUND_TEST_REDIS_ADDR（可为空表示无 Redis，降级走纯 DB）；
// AES 密钥为测试进程内随机生成，不落盘、不进生产库。

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

var (
	testSrv *Server
	testDB  *sql.DB
)

const (
	testAdmin = "test-admin-secret"
)

// stubVerifier：测试用 IM token 校验器：token "imtok-<uid>" 解析为 uid；
// "imtok-bad" 与其他格式一律无效。生产用 HTTPTokenVerifier（调 OpenIM parse_token）。
type stubVerifier struct{}

func (stubVerifier) VerifyToken(ctx context.Context, token string) (string, error) {
	if token == "imtok-bad" {
		return "", errTokenInvalid
	}
	if strings.HasPrefix(token, "imtok-") {
		uid := strings.TrimPrefix(token, "imtok-")
		if uid != "" {
			return uid, nil
		}
	}
	return "", errTokenInvalid
}

type downVerifier struct{}

func (downVerifier) VerifyToken(ctx context.Context, token string) (string, error) {
	return "", errVerifierDown
}

func TestMain(m *testing.M) {
	adminURL := os.Getenv("FUND_TEST_ADMIN_URL")
	if adminURL == "" {
		fmt.Fprintln(os.Stderr, "FUND_TEST_ADMIN_URL is empty, skipping fund tests")
		os.Exit(0)
	}
	// 1) 连 maintenance 库建测试库
	maintURL := replaceDB(adminURL, "postgres")
	maint, err := sql.Open("postgres", maintURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open maint:", err)
		os.Exit(1)
	}
	_, _ = maint.Exec(`CREATE DATABASE xianliao_fund_test`)
	maint.Close()
	// 2) 连测试库跑迁移 up（0001 -> 0002 -> 0003）
	db, err := sql.Open("postgres", adminURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open test db:", err)
		os.Exit(1)
	}
	testDB = db
	for _, f := range []string{"0001_init.up.sql", "0002_recovery.up.sql", "0003_redpacket.up.sql", "0004_group_redpacket.up.sql", "0005_group_lucky_redpacket.up.sql", "0006_transfer.up.sql", "0007_admin.up.sql", "0008_recharge.up.sql", "0009_withdraw.up.sql", "0010_manual_gateway.up.sql", "0011_withdraw_qr.up.sql", "0012_gateway_channels.up.sql", "0013_balance_adjust.up.sql", "0014_phone_and_name.up.sql", "0015_tx_password_admin_reset.up.sql"} {
		upSQL, err := os.ReadFile("../../migrations/" + f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read migration:", err)
			os.Exit(1)
		}
		if _, err := db.Exec(string(upSQL)); err != nil {
			fmt.Fprintln(os.Stderr, "apply migration:", err)
			os.Exit(1)
		}
	}
	// 3) 建测试服务（AES 密钥进程内随机；IM 校验用 stub：token "imtok-<uid>" 解析为 uid）
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	_ = key
	uploadDir, err := os.MkdirTemp("", "fund-uploads-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp upload dir:", err)
		os.Exit(1)
	}
	cfg := Config{
		Addr:          "127.0.0.1:0",
		DatabaseURL:   adminURL, // 测试库内以管理员连接跑业务逻辑；权限强制另由 Test14 的 has_table_privilege 校验
		RedisAddr:     os.Getenv("FUND_TEST_REDIS_ADDR"),
		AdminSecret:   testAdmin,
		AESKey:        key,
		TokenVerifier: stubVerifier{},
		// Phase 8：测试服务开启 mock 回调入口（仅测试库 xianliao_fund_test，绝不指向生产库）；
		// 密钥为测试进程内固定夹具，不等于生产密钥，生产 fund.env 必须不开启该开关。
		MockPaymentEnabled: true,
		MockCallbackSecret: testMockSecret,
		// 0010：上传目录为测试进程内临时目录；网关适配器为测试 stub（生产默认无适配器 = 通道关闭）。
		UploadDir:      uploadDir,
		GatewayAdapter: stubGatewayAdapter{secret: testGatewaySecret},
	}
	// 随机 AES 密钥需以同样字节供服务使用：上面 key 已生成，直接用
	testSrv = NewServer(cfg, db)
	code := m.Run()
	// 跑后清理：清空全部资金表并重插平台户
	_, _ = db.Exec(`TRUNCATE fund_admin_adjustment, fund_risk_limit, fund_withdraw_config, transfer_order, group_lucky_redpacket_member, group_lucky_redpacket_claim, group_lucky_redpacket_share, group_lucky_redpacket_packet, group_redpacket_member, group_redpacket_claim, group_redpacket_share, group_redpacket_packet, redpacket_claim, redpacket_packet, wallet_ledger, wallet_audit_log, wallet_withdraw_method, wallet_tx_password, wallet_tx_password_recovery, wallet_freeze, wallet_order, wallet_txn, wallet_account, wallet_payment_method, wallet_gateway_channel, fund_upload_file, fund_balance_adjustment, user_phone_binding, recharge_proof RESTART IDENTITY CASCADE`)
	_, _ = db.Exec(`INSERT INTO wallet_account (user_id) VALUES ('platform:settlement') ON CONFLICT (user_id) DO NOTHING`)
	db.Close()
	os.Exit(code)
}

func replaceDB(raw, dbname string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Path = "/" + dbname
	return u.String()
}

func uniq(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%x", prefix, b)
}

// ---- HTTP 测试助手 ----

type apiResp struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func call(t *testing.T, method, path, uid string, body any, admin bool) (int, apiResp) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if admin {
		req.Header.Set("X-Admin-Secret", testAdmin)
	} else {
		req.Header.Set("Authorization", "Bearer imtok-"+uid)
	}
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	var resp apiResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func mustWallet(t *testing.T, uid string) {
	t.Helper()
	code, resp := call(t, "POST", "/v1/wallet/create", uid, map[string]any{}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("create wallet %s: code=%d resp=%s", uid, code, string(resp.Data))
	}
}

func balance(t *testing.T, uid string) (int64, int64) {
	t.Helper()
	code, resp := call(t, "GET", "/v1/wallet/balance", uid, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("balance %s: code=%d", uid, code)
	}
	var a Account
	if err := json.Unmarshal(resp.Data, &a); err != nil {
		t.Fatal(err)
	}
	return a.BalanceCents, a.FrozenCents
}

func ledgerSumZero(t *testing.T, txnID string) {
	t.Helper()
	rows, err := testDB.Query(`SELECT direction, amount_cents FROM wallet_ledger WHERE txn_id=$1`, txnID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sum int64
	var n int
	for rows.Next() {
		var dir int
		var amt int64
		if err := rows.Scan(&dir, &amt); err != nil {
			t.Fatal(err)
		}
		sum += signed(dir, amt)
		n++
	}
	if n < 2 {
		t.Fatalf("txn %s has %d entries, want >=2", txnID, n)
	}
	if sum != 0 {
		t.Fatalf("txn %s signed sum = %d, want 0", txnID, sum)
	}
}

// 01 钱包创建（幂等：两次创建余额不变、同一行）
func Test01WalletCreate(t *testing.T) {
	uid := uniq("u01")
	mustWallet(t, uid)
	mustWallet(t, uid)
	b, f := balance(t, uid)
	if b != 0 || f != 0 {
		t.Fatalf("new wallet balance=%d frozen=%d, want 0/0", b, f)
	}
}

// 02 余额查询
func Test02Balance(t *testing.T) {
	uid := uniq("u02")
	mustWallet(t, uid)
	code, resp := call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": 12345, "idempotency_key": uniq("idem")}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("income code=%d err=%v", code, resp.Error)
	}
	b, f := balance(t, uid)
	if b != 12345 || f != 0 {
		t.Fatalf("balance=%d frozen=%d, want 12345/0", b, f)
	}
}

// 03 双重记账（收入一笔 = 用户 + 平台两条分录，带符号合计 0；对账 diff 0）
func Test03DoubleEntry(t *testing.T) {
	uid := uniq("u03")
	mustWallet(t, uid)
	code, resp := call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": 5000, "idempotency_key": uniq("idem")}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("income code=%d", code)
	}
	var txn Txn
	if err := json.Unmarshal(resp.Data, &txn); err != nil {
		t.Fatal(err)
	}
	ledgerSumZero(t, txn.TxnID)
	// 对账：触发后 diff 必须为 0
	code, resp = call(t, "POST", "/v1/admin/reconcile", "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("reconcile code=%d err=%v", code, resp.Error)
	}
	var rec ReconcileResult
	if err := json.Unmarshal(resp.Data, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.DiffCents != 0 || rec.Status != 0 {
		t.Fatalf("reconcile diff=%d status=%d, want 0/0", rec.DiffCents, rec.Status)
	}
	// 结果查询
	code, resp = call(t, "GET", "/v1/admin/reconcile/"+rec.ReconcileID, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("get reconcile code=%d", code)
	}
}

// 04 收入 + 退款 + 冲正
func Test04IncomeRefundReversal(t *testing.T) {
	uid := uniq("u04")
	mustWallet(t, uid)
	code, resp := call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": 10000, "idempotency_key": uniq("idem")}, false)
	if code != 200 {
		t.Fatalf("income code=%d", code)
	}
	var inc Txn
	_ = json.Unmarshal(resp.Data, &inc)
	code, _ = call(t, "POST", "/v1/ledger/refund", uid, map[string]any{"amount_cents": 2000, "idempotency_key": uniq("idem")}, false)
	if code != 200 {
		t.Fatalf("refund code=%d", code)
	}
	b, _ := balance(t, uid)
	if b != 12000 {
		t.Fatalf("after refund balance=%d, want 12000", b)
	}
	code, resp = call(t, "POST", "/v1/ledger/reverse", uid, map[string]any{"original_txn_id": inc.TxnID, "idempotency_key": uniq("idem")}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("reverse code=%d err=%v", code, resp.Error)
	}
	b, _ = balance(t, uid)
	if b != 2000 {
		t.Fatalf("after reversal balance=%d, want 2000", b)
	}
}

// 05 支出（含余额不足拒绝且余额不变）
func Test05Expense(t *testing.T) {
	uid := uniq("u05")
	mustWallet(t, uid)
	call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": 3000, "idempotency_key": uniq("idem")}, false)
	code, _ := call(t, "POST", "/v1/ledger/expense", uid, map[string]any{"amount_cents": 1000, "idempotency_key": uniq("idem")}, false)
	if code != 200 {
		t.Fatalf("expense code=%d", code)
	}
	b, _ := balance(t, uid)
	if b != 2000 {
		t.Fatalf("balance=%d, want 2000", b)
	}
	code, resp := call(t, "POST", "/v1/ledger/expense", uid, map[string]any{"amount_cents": 99999, "idempotency_key": uniq("idem")}, false)
	if code == 200 && resp.OK {
		t.Fatalf("overspend unexpectedly succeeded")
	}
	b, _ = balance(t, uid)
	if b != 2000 {
		t.Fatalf("balance after failed expense=%d, want 2000", b)
	}
}

// 06 冻结 / 解冻 / 冻结扣款 / 冻结退款
func Test06FreezeUnfreeze(t *testing.T) {
	uid := uniq("u06")
	mustWallet(t, uid)
	call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": 10000, "idempotency_key": uniq("idem")}, false)
	code, resp := call(t, "POST", "/v1/ledger/freeze", uid, map[string]any{"amount_cents": 4000, "idempotency_key": uniq("idem")}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("freeze code=%d err=%v", code, resp.Error)
	}
	var fr struct {
		Txn      Txn    `json:"txn"`
		FreezeID string `json:"freeze_id"`
	}
	if err := json.Unmarshal(resp.Data, &fr); err != nil {
		t.Fatal(err)
	}
	b, f := balance(t, uid)
	if b != 6000 || f != 4000 {
		t.Fatalf("after freeze balance=%d frozen=%d, want 6000/4000", b, f)
	}
	// 解冻 1000
	code, _ = call(t, "POST", "/v1/ledger/unfreeze", uid, map[string]any{"freeze_id": fr.FreezeID, "amount_cents": 1000}, false)
	if code != 200 {
		t.Fatalf("unfreeze code=%d", code)
	}
	b, f = balance(t, uid)
	if b != 7000 || f != 3000 {
		t.Fatalf("after unfreeze balance=%d frozen=%d, want 7000/3000", b, f)
	}
	// 冻结扣款 1000（冻结 -> 平台）
	code, _ = call(t, "POST", "/v1/ledger/freeze-charge", uid, map[string]any{"freeze_id": fr.FreezeID, "amount_cents": 1000}, false)
	if code != 200 {
		t.Fatalf("freeze-charge code=%d", code)
	}
	b, f = balance(t, uid)
	if b != 7000 || f != 2000 {
		t.Fatalf("after charge balance=%d frozen=%d, want 7000/2000", b, f)
	}
	// 冻结退款 2000（冻结 -> 可用，剩余清零）
	code, _ = call(t, "POST", "/v1/ledger/freeze-refund", uid, map[string]any{"freeze_id": fr.FreezeID, "amount_cents": 2000}, false)
	if code != 200 {
		t.Fatalf("freeze-refund code=%d", code)
	}
	b, f = balance(t, uid)
	if b != 9000 || f != 0 {
		t.Fatalf("after freeze-refund balance=%d frozen=%d, want 9000/0", b, f)
	}
	// 超冻结额拒绝
	code, _ = call(t, "POST", "/v1/ledger/unfreeze", uid, map[string]any{"freeze_id": fr.FreezeID, "amount_cents": 1}, false)
	if code == 200 {
		t.Fatalf("over-freeze unfreeze unexpectedly succeeded")
	}
}

// 07 重复请求（同一幂等键两次，只记账一次）
func Test07IdempotentRepeat(t *testing.T) {
	uid := uniq("u07")
	mustWallet(t, uid)
	idem := uniq("idem")
	code, resp1 := call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": 7777, "idempotency_key": idem}, false)
	if code != 200 {
		t.Fatalf("first income code=%d", code)
	}
	code, resp2 := call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": 7777, "idempotency_key": idem}, false)
	if code != 200 {
		t.Fatalf("repeat income code=%d", code)
	}
	var t1, t2 Txn
	_ = json.Unmarshal(resp1.Data, &t1)
	_ = json.Unmarshal(resp2.Data, &t2)
	if t1.TxnID != t2.TxnID {
		t.Fatalf("idempotent repeat txn differ: %s vs %s", t1.TxnID, t2.TxnID)
	}
	b, _ := balance(t, uid)
	if b != 7777 {
		t.Fatalf("balance=%d, want 7777 (no double credit)", b)
	}
}

// 08 并发扣款（20 并发各扣 100，余额 1000，只放行 10 笔，零超扣）
func Test08ConcurrentDebit(t *testing.T) {
	uid := uniq("u08")
	mustWallet(t, uid)
	call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": 1000, "idempotency_key": uniq("idem")}, false)
	// 注意：Redis 辅助锁被持有时会按设计回 429（不得静默放行）；真实客户端应重试。
	// 本测试模拟该行为：429 时换新幂等键重试，直至成功或余额不足（非 429 失败即停止）。
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 100; attempt++ {
				code, _ := call(t, "POST", "/v1/ledger/expense", uid, map[string]any{"amount_cents": 100, "idempotency_key": uniq("idem")}, false)
				if code == 200 {
					mu.Lock()
					success++
					mu.Unlock()
					return
				}
				if code != 429 {
					return // 余额不足等业务拒绝：停止重试
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	if success != 10 {
		t.Fatalf("concurrent success=%d, want 10", success)
	}
	b, _ := balance(t, uid)
	if b != 0 {
		t.Fatalf("final balance=%d, want 0", b)
	}
	if b < 0 {
		t.Fatalf("negative balance!")
	}
}

// 09 负余额防护（超额支出/冻结/负金额均拒绝，余额不变）
func Test09NegativeBalanceProtection(t *testing.T) {
	uid := uniq("u09")
	mustWallet(t, uid)
	call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": 500, "idempotency_key": uniq("idem")}, false)
	code, _ := call(t, "POST", "/v1/ledger/expense", uid, map[string]any{"amount_cents": 501, "idempotency_key": uniq("idem")}, false)
	if code == 200 {
		t.Fatalf("overspend succeeded")
	}
	code, _ = call(t, "POST", "/v1/ledger/freeze", uid, map[string]any{"amount_cents": 501, "idempotency_key": uniq("idem")}, false)
	if code == 200 {
		t.Fatalf("over-freeze succeeded")
	}
	code, _ = call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": -100, "idempotency_key": uniq("idem")}, false)
	if code == 200 {
		t.Fatalf("negative amount succeeded")
	}
	// DB 层 CHECK 约束：直接写负余额必须失败
	_, err := testDB.Exec(`UPDATE wallet_account SET balance_cents=-1 WHERE user_id=$1`, uid)
	if err == nil {
		t.Fatalf("CHECK constraint did not reject negative balance")
	}
	b, f := balance(t, uid)
	if b != 500 || f != 0 {
		t.Fatalf("balance=%d frozen=%d, want 500/0", b, f)
	}
}

// 10 交易密码（设置两次不一致拒绝、非 6 位拒绝、设置/验密/修改全流程）
func Test10TxPassword(t *testing.T) {
	uid := uniq("u10")
	mustWallet(t, uid)
	code, _ := call(t, "POST", "/v1/tx-password/set", uid, map[string]any{"password": "123456", "confirm_password": "654321"}, false)
	if code == 200 {
		t.Fatalf("mismatched passwords unexpectedly set")
	}
	code, _ = call(t, "POST", "/v1/tx-password/set", uid, map[string]any{"password": "12345a", "confirm_password": "12345a"}, false)
	if code == 200 {
		t.Fatalf("non-digit password unexpectedly set")
	}
	code, _ = call(t, "POST", "/v1/tx-password/set", uid, map[string]any{"password": "123456", "confirm_password": "123456"}, false)
	if code != 200 {
		t.Fatalf("set password code=%d", code)
	}
	code, resp := call(t, "POST", "/v1/tx-password/verify", uid, map[string]any{"password": "123456"}, false)
	if code != 200 {
		t.Fatalf("verify code=%d err=%v", code, resp.Error)
	}
	code, _ = call(t, "POST", "/v1/tx-password/change", uid, map[string]any{"old_password": "000000", "new_password": "111111", "confirm_password": "111111"}, false)
	if code == 200 {
		t.Fatalf("change with wrong old password succeeded")
	}
	code, _ = call(t, "POST", "/v1/tx-password/change", uid, map[string]any{"old_password": "123456", "new_password": "111111", "confirm_password": "111111"}, false)
	if code != 200 {
		t.Fatalf("change code=%d", code)
	}
	code, resp = call(t, "POST", "/v1/tx-password/verify", uid, map[string]any{"password": "111111"}, false)
	if code != 200 {
		t.Fatalf("verify new code=%d", code)
	}
	// 哈希非明文：库中 password_hash 不等于明文
	var hash string
	if err := testDB.QueryRow(`SELECT password_hash FROM wallet_tx_password WHERE user_id=$1`, uid).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == "111111" || len(hash) < 4 || hash[:4] != "$2a$" {
		t.Fatalf("password_hash is not bcrypt")
	}
}

// 11 错误密码锁定（连续错 5 次锁定 30 分钟，锁定期间正确密码亦拒绝）
func Test11PasswordLock(t *testing.T) {
	uid := uniq("u11")
	mustWallet(t, uid)
	call(t, "POST", "/v1/tx-password/set", uid, map[string]any{"password": "222222", "confirm_password": "222222"}, false)
	for i := 0; i < 5; i++ {
		code, resp := call(t, "POST", "/v1/tx-password/verify", uid, map[string]any{"password": "000000"}, false)
		if code != 200 {
			t.Fatalf("verify attempt %d code=%d", i+1, code)
		}
		var v struct {
			Verified bool `json:"verified"`
			Locked   bool `json:"locked"`
		}
		_ = json.Unmarshal(resp.Data, &v)
		if v.Verified {
			t.Fatalf("wrong password attempt %d unexpectedly verified", i+1)
		}
	}
	code, resp := call(t, "GET", "/v1/tx-password/status", uid, nil, false)
	if code != 200 {
		t.Fatalf("status code=%d", code)
	}
	var st TxPasswordStatus
	if err := json.Unmarshal(resp.Data, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Locked {
		t.Fatalf("expected locked after 5 errors, got locked=%v error_count=%d", st.Locked, st.ErrorCount)
	}
	if st.LockedUntil == nil {
		t.Fatalf("locked_until is nil")
	}
	// 锁定期间正确密码也拒绝（200 + verified=false + locked=true）
	code, resp = call(t, "POST", "/v1/tx-password/verify", uid, map[string]any{"password": "222222"}, false)
	if code != 200 {
		t.Fatalf("verify while locked code=%d", code)
	}
	var v struct {
		Verified bool `json:"verified"`
		Locked   bool `json:"locked"`
	}
	_ = json.Unmarshal(resp.Data, &v)
	if v.Verified {
		t.Fatalf("correct password verified while locked")
	}
	if !v.Locked {
		t.Fatalf("verify response locked=%v, want true", v.Locked)
	}
}

// 12 提现账户绑定（三种类型、脱敏、唯一默认、设默认切换、换绑验密）
func Test12WithdrawMethod(t *testing.T) {
	uid := uniq("u12")
	mustWallet(t, uid)
	call(t, "POST", "/v1/tx-password/set", uid, map[string]any{"password": "333333", "confirm_password": "333333"}, false)
	code, resp := call(t, "POST", "/v1/withdraw-methods", uid, map[string]any{"type": 1, "real_name": "张三", "account_no": "zhangsan@alipay.example"}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("add alipay code=%d err=%v", code, resp.Error)
	}
	var m1 WithdrawMethod
	_ = json.Unmarshal(resp.Data, &m1)
	if !m1.IsDefault {
		t.Fatalf("first method should auto-default")
	}
	if strings.Contains(m1.AccountNo, "zhangsan") {
		t.Fatalf("account_no not masked: %s", m1.AccountNo)
	}
	if !strings.HasSuffix(m1.AccountNo, "mple") && !strings.HasSuffix(m1.AccountNo, "ample") {
		// 脱敏应为 **** + 后 4 位；alipay 账号后 4 位为 "mple"
		t.Fatalf("masked account_no unexpected: %s", m1.AccountNo)
	}
	code, resp = call(t, "POST", "/v1/withdraw-methods", uid, map[string]any{"type": 3, "real_name": "张三", "account_no": "6222021234567890", "bank_name": "工商银行", "bank_branch": "北京分行"}, false)
	if code != 200 {
		t.Fatalf("add bank code=%d err=%v", code, resp.Error)
	}
	var m2 WithdrawMethod
	_ = json.Unmarshal(resp.Data, &m2)
	if m2.AccountNo != "****7890" {
		t.Fatalf("bank masked=%q, want ****7890", m2.AccountNo)
	}
	// 设默认切换（须验交易密码）
	code, _ = call(t, "POST", "/v1/withdraw-methods/"+m2.MethodID+"/set-default", uid, map[string]any{"tx_password": "333333"}, false)
	if code != 200 {
		t.Fatalf("set-default code=%d", code)
	}
	code, resp = call(t, "GET", "/v1/withdraw-methods", uid, nil, false)
	if code != 200 {
		t.Fatalf("list code=%d", code)
	}
	var list []WithdrawMethod
	_ = json.Unmarshal(resp.Data, &list)
	defaults := 0
	for _, m := range list {
		if m.IsDefault {
			defaults++
			if m.MethodID != m2.MethodID {
				t.Fatalf("default is %s, want %s", m.MethodID, m2.MethodID)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("defaults=%d, want exactly 1", defaults)
	}
	// 换绑须验密：错密拒绝
	code, _ = call(t, "PUT", "/v1/withdraw-methods/"+m2.MethodID, uid, map[string]any{"bank_branch": "上海分行", "tx_password": "000000"}, false)
	if code == 200 {
		t.Fatalf("update with wrong tx password succeeded")
	}
	code, _ = call(t, "PUT", "/v1/withdraw-methods/"+m2.MethodID, uid, map[string]any{"bank_branch": "上海分行", "tx_password": "333333"}, false)
	if code != 200 {
		t.Fatalf("update code=%d", code)
	}
	// 解绑须验密
	code, _ = call(t, "DELETE", "/v1/withdraw-methods/"+m1.MethodID, uid, map[string]any{"tx_password": "333333"}, false)
	if code != 200 {
		t.Fatalf("delete code=%d", code)
	}
	// 准入：绑了方式 + 设了密码 -> ok
	code, resp = call(t, "GET", "/v1/finance/status", uid, nil, false)
	if code != 200 {
		t.Fatalf("finance status code=%d", code)
	}
	var fs FinanceStatus
	_ = json.Unmarshal(resp.Data, &fs)
	if fs.FinanceAccountStatus != "ok" || !fs.HasWithdrawalMethod || !fs.HasTransactionPassword {
		t.Fatalf("finance status %+v, want ok/true/true", fs)
	}
	// 准入顺序：新用户无方式无密码 -> need_method
	uid2 := uniq("u12b")
	mustWallet(t, uid2)
	code, resp = call(t, "GET", "/v1/finance/status", uid2, nil, false)
	var fs2 FinanceStatus
	_ = json.Unmarshal(resp.Data, &fs2)
	if fs2.FinanceAccountStatus != "need_method" {
		t.Fatalf("new user finance status=%s, want need_method", fs2.FinanceAccountStatus)
	}
}

// 13 用户权限隔离（越权 403：请求头用户 ≠ body/query 用户；无密钥 401；平台户 403）
func Test13PermissionIsolation(t *testing.T) {
	uidA := uniq("u13a")
	uidB := uniq("u13b")
	mustWallet(t, uidA)
	mustWallet(t, uidB)
	// A 试图以 body user_id=B 操作
	code, _ := call(t, "POST", "/v1/ledger/income", uidA, map[string]any{"user_id": uidB, "amount_cents": 100, "idempotency_key": uniq("idem")}, false)
	if code != 403 {
		t.Fatalf("body user mismatch code=%d, want 403", code)
	}
	// query user_id 越权（与 token 用户不一致）
	req := httptest.NewRequest("GET", "/v1/wallet/balance?user_id="+uidB, nil)
	req.Header.Set("Authorization", "Bearer imtok-"+uidA)
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("query user mismatch code=%d, want 403", rec.Code)
	}
	// 无 token
	req2 := httptest.NewRequest("GET", "/v1/wallet/balance", nil)
	rec2 := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != 401 {
		t.Fatalf("missing token code=%d, want 401", rec2.Code)
	}
	// 伪造 token
	req2b := httptest.NewRequest("GET", "/v1/wallet/balance", nil)
	req2b.Header.Set("Authorization", "Bearer imtok-bad")
	rec2b := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec2b, req2b)
	if rec2b.Code != 401 {
		t.Fatalf("bad token code=%d, want 401", rec2b.Code)
	}
	// 平台户禁止操作
	code, _ = call(t, "GET", "/v1/wallet/balance", PlatformUserID, nil, false)
	if code != 403 {
		t.Fatalf("platform access code=%d, want 403", code)
	}
	// 管理接口无 admin 密钥拒绝
	req3 := httptest.NewRequest("POST", "/v1/admin/reconcile", nil)
	rec3 := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec3, req3)
	if rec3.Code != 401 {
		t.Fatalf("admin without secret code=%d, want 401", rec3.Code)
	}
}

// 14 迁移与回滚（逆序 down 后表消失，顺序 up 后结构与平台户恢复，可重新建钱包记账）
func Test14MigrationRollback(t *testing.T) {
	for _, f := range []string{"0015_tx_password_admin_reset.down.sql", "0014_phone_and_name.down.sql", "0013_balance_adjust.down.sql", "0011_withdraw_qr.down.sql", "0010_manual_gateway.down.sql", "0009_withdraw.down.sql", "0008_recharge.down.sql", "0007_admin.down.sql", "0006_transfer.down.sql", "0005_group_lucky_redpacket.down.sql", "0004_group_redpacket.down.sql", "0003_redpacket.down.sql", "0002_recovery.down.sql", "0001_init.down.sql"} {
		downSQL, err := readMigrationFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testDB.Exec(downSQL); err != nil {
			t.Fatalf("down %s: %v", f, err)
		}
	}
	var tbl string
	err := testDB.QueryRow(`SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name='wallet_account'`).Scan(&tbl)
	if err != sql.ErrNoRows {
		t.Fatalf("after down wallet_account still exists (err=%v)", err)
	}
	for _, f := range []string{"0001_init.up.sql", "0002_recovery.up.sql", "0003_redpacket.up.sql", "0004_group_redpacket.up.sql", "0005_group_lucky_redpacket.up.sql", "0006_transfer.up.sql", "0007_admin.up.sql", "0008_recharge.up.sql", "0009_withdraw.up.sql", "0010_manual_gateway.up.sql", "0011_withdraw_qr.up.sql", "0012_gateway_channels.up.sql", "0013_balance_adjust.up.sql", "0014_phone_and_name.up.sql", "0015_tx_password_admin_reset.up.sql"} {
		upSQL, err := readMigrationFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testDB.Exec(upSQL); err != nil {
			t.Fatalf("up %s: %v", f, err)
		}
	}
	// 0002 表恢复
	err = testDB.QueryRow(`SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name='wallet_tx_password_recovery'`).Scan(&tbl)
	if err != nil || tbl != "wallet_tx_password_recovery" {
		t.Fatalf("after up recovery table missing (err=%v)", err)
	}
	// 0010 表恢复
	err = testDB.QueryRow(`SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name='wallet_payment_method'`).Scan(&tbl)
	if err != nil || tbl != "wallet_payment_method" {
		t.Fatalf("after up payment method table missing (err=%v)", err)
	}
	// 0003 表恢复
	err = testDB.QueryRow(`SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name='redpacket_packet'`).Scan(&tbl)
	if err != nil || tbl != "redpacket_packet" {
		t.Fatalf("after up redpacket table missing (err=%v)", err)
	}
	// 0004 表恢复
	err = testDB.QueryRow(`SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name='group_redpacket_packet'`).Scan(&tbl)
	if err != nil || tbl != "group_redpacket_packet" {
		t.Fatalf("after up group redpacket table missing (err=%v)", err)
	}
	// 0005 表恢复
	err = testDB.QueryRow(`SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name='group_lucky_redpacket_packet'`).Scan(&tbl)
	if err != nil || tbl != "group_lucky_redpacket_packet" {
		t.Fatalf("after up lucky redpacket table missing (err=%v)", err)
	}
	// 0006 表恢复
	err = testDB.QueryRow(`SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name='transfer_order'`).Scan(&tbl)
	if err != nil || tbl != "transfer_order" {
		t.Fatalf("after up transfer table missing (err=%v)", err)
	}
	// 0007 表恢复（调整单 + 限额配置 + wallet_order 审核字段）
	err = testDB.QueryRow(`SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name='fund_admin_adjustment'`).Scan(&tbl)
	if err != nil || tbl != "fund_admin_adjustment" {
		t.Fatalf("after up adjustment table missing (err=%v)", err)
	}
	err = testDB.QueryRow(`SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name='fund_risk_limit'`).Scan(&tbl)
	if err != nil || tbl != "fund_risk_limit" {
		t.Fatalf("after up risk limit table missing (err=%v)", err)
	}
	var col string
	err = testDB.QueryRow(`SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name='wallet_order' AND column_name='review_first_admin_id'`).Scan(&col)
	if err != nil || col != "review_first_admin_id" {
		t.Fatalf("after up wallet_order review column missing (err=%v)", err)
	}
	// 0009 提现字段与配置表恢复
	for _, wantCol := range []string{"freeze_id", "payout_proof_no", "payout_at", "payout_operator_id", "payout_note"} {
		err = testDB.QueryRow(`SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name='wallet_order' AND column_name=$1`, wantCol).Scan(&col)
		if err != nil || col != wantCol {
			t.Fatalf("after up wallet_order withdraw column %s missing (err=%v)", wantCol, err)
		}
	}
	err = testDB.QueryRow(`SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name='fund_withdraw_config'`).Scan(&tbl)
	if err != nil || tbl != "fund_withdraw_config" {
		t.Fatalf("after up withdraw config table missing (err=%v)", err)
	}
	// 0008 充值字段恢复
	for _, wantCol := range []string{"channel", "paid_txn_id", "refund_txn_id", "failure_reason", "client_idempotency_key"} {
		err = testDB.QueryRow(`SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name='wallet_order' AND column_name=$1`, wantCol).Scan(&col)
		if err != nil || col != wantCol {
			t.Fatalf("after up wallet_order recharge column %s missing (err=%v)", wantCol, err)
		}
	}
	// 权限强制校验：fund_app 对 ledger/audit_log 无 UPDATE 权限（数据库层 append-only）
	var canUpdate bool
	if err := testDB.QueryRow(`SELECT has_table_privilege('fund_app','wallet_ledger','UPDATE')`).Scan(&canUpdate); err != nil {
		t.Fatalf("privilege check: %v", err)
	}
	if canUpdate {
		t.Fatalf("fund_app still has UPDATE on wallet_ledger")
	}
	if err := testDB.QueryRow(`SELECT has_table_privilege('fund_app','wallet_audit_log','DELETE')`).Scan(&canUpdate); err != nil {
		t.Fatalf("privilege check audit: %v", err)
	}
	if canUpdate {
		t.Fatalf("fund_app still has DELETE on wallet_audit_log")
	}
	// 平台户重建
	var cnt int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_account WHERE user_id='platform:settlement'`).Scan(&cnt); err != nil || cnt != 1 {
		t.Fatalf("platform account count=%d err=%v, want 1", cnt, err)
	}
	// down 清空了旧数据是预期行为（回滚即删表）；up 后新钱包可正常记账
	uid := uniq("u14")
	mustWallet(t, uid)
	code, _ := call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": 100, "idempotency_key": uniq("idem")}, false)
	if code != 200 {
		t.Fatalf("post-rollback income code=%d", code)
	}
}

func readMigrationFile(name string) (string, error) {
	b, err := os.ReadFile("../../migrations/" + name)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// 15 服务端异常恢复（新服务实例连同一库：余额与流水不变；未提交事务不落库）
func Test15ServerRecovery(t *testing.T) {
	uid := uniq("u15")
	mustWallet(t, uid)
	call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": 4321, "idempotency_key": uniq("idem")}, false)
	// 未提交事务：Begin 后 INSERT 再 Rollback，余额与流水不得变化
	tx, err := testDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents) VALUES ('txn_uncommitted', $1, 1, 2, 9999, 9999, 0)`, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var cnt int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_ledger WHERE txn_id='txn_uncommitted'`).Scan(&cnt); err != nil || cnt != 0 {
		t.Fatalf("uncommitted ledger count=%d err=%v, want 0", cnt, err)
	}
	// 模拟重启：新建服务实例（同一 DB、同一密钥配置）查询余额与流水
	key := testSrv.cfg.AESKey
	cfg2 := Config{Addr: "127.0.0.1:0", DatabaseURL: testSrv.cfg.DatabaseURL, RedisAddr: testSrv.cfg.RedisAddr, AdminSecret: testAdmin, AESKey: key, TokenVerifier: stubVerifier{}}
	srv2 := NewServer(cfg2, testDB)
	req := httptest.NewRequest("GET", "/v1/wallet/balance", nil)
	req.Header.Set("Authorization", "Bearer imtok-"+uid)
	rec := httptest.NewRecorder()
	srv2.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("restarted server balance code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp apiResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	var a Account
	_ = json.Unmarshal(resp.Data, &a)
	if a.BalanceCents != 4321 {
		t.Fatalf("restarted balance=%d, want 4321", a.BalanceCents)
	}
	// 流水在重启后仍可查到收入一笔
	req2 := httptest.NewRequest("GET", "/v1/wallet/ledger?limit=10", nil)
	req2.Header.Set("Authorization", "Bearer imtok-"+uid)
	rec2 := httptest.NewRecorder()
	srv2.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("restarted ledger code=%d", rec2.Code)
	}
}
