package fund

// Phase 6 测试：用户之间余额转账（即时到账）66–81。全部用 stubVerifier（token "imtok-<uid>"），
// 测试库与命令与 Phase 1–5 相同。注资用内部 income 接口（真实充值在 Phase 8，测试注资不走支付渠道）。

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func sendTransferCall(t *testing.T, sender, receiver string, amount int64, idem string, pwd string) (int, apiResp) {
	t.Helper()
	body := map[string]any{"receiver_id": receiver, "amount_cents": amount, "remark": "转账", "tx_password": pwd}
	if idem != "" {
		body["idempotency_key"] = idem
	}
	return call(t, "POST", "/v1/transfers", sender, body, false)
}

func sendTransferOK(t *testing.T, sender, receiver string, amount int64, idem string) TransferOrder {
	t.Helper()
	code, resp := sendTransferCall(t, sender, receiver, amount, idem, testTxPwd)
	if code != 200 || !resp.OK {
		t.Fatalf("transfer %s->%s %d: code=%d err=%+v", sender, receiver, amount, code, resp.Error)
	}
	var o TransferOrder
	if err := json.Unmarshal(resp.Data, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func ledgerTxnsForUser(t *testing.T, uid string) []LedgerEntry {
	t.Helper()
	code, resp := call(t, "GET", "/v1/wallet/ledger?limit=200", uid, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("ledger %s: code=%d", uid, code)
	}
	var entries []LedgerEntry
	if err := json.Unmarshal(resp.Data, &entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

// 66 转账成功：双方余额、分录合计为 0、订单记录、流水可查（双方流水均含该 txn，对手方正确）。
func Test66TransferSuccess(t *testing.T) {
	a := uniq("t66a")
	b := uniq("t66b")
	admitUser(t, a)
	fundUser(t, a, 10000)
	mustWallet(t, b)
	o := sendTransferOK(t, a, b, 4000, uniq("idem"))
	if o.Status != TransferSuccess || o.SenderID != a || o.ReceiverID != b || o.AmountCents != 4000 || o.TxnID == nil {
		t.Fatalf("order %+v unexpected", o)
	}
	ba, _ := balance(t, a)
	bb, _ := balance(t, b)
	if ba != 6000 || bb != 4000 {
		t.Fatalf("balances a=%d b=%d, want 6000/4000", ba, bb)
	}
	ledgerSumZero(t, *o.TxnID)
	// 双方流水均含该 txn，且对手方字段指向对方。
	foundA, foundB := false, false
	for _, e := range ledgerTxnsForUser(t, a) {
		if e.TxnID == *o.TxnID {
			foundA = true
			if e.Direction != DirDebit || e.OppositeUserID == nil || *e.OppositeUserID != b {
				t.Fatalf("sender entry %+v unexpected", e)
			}
		}
	}
	for _, e := range ledgerTxnsForUser(t, b) {
		if e.TxnID == *o.TxnID {
			foundB = true
			if e.Direction != DirCredit || e.OppositeUserID == nil || *e.OppositeUserID != a {
				t.Fatalf("receiver entry %+v unexpected", e)
			}
		}
	}
	if !foundA || !foundB {
		t.Fatalf("transfer txn missing in ledgers: sender=%v receiver=%v", foundA, foundB)
	}
	// 详情双方可见。
	code, _ := call(t, "GET", "/v1/transfers/"+o.OrderID, a, nil, false)
	if code != 200 {
		t.Fatalf("sender detail code=%d", code)
	}
	code, _ = call(t, "GET", "/v1/transfers/"+o.OrderID, b, nil, false)
	if code != 200 {
		t.Fatalf("receiver detail code=%d", code)
	}
}

// 67 余额不足：400 INSUFFICIENT_BALANCE、双方余额不动、无成功订单、无 type 18 txn、失败审计行 status=2。
func Test67TransferInsufficient(t *testing.T) {
	a := uniq("t67a")
	b := uniq("t67b")
	admitUser(t, a)
	fundUser(t, a, 100)
	mustWallet(t, b)
	code, resp := sendTransferCall(t, a, b, 101, uniq("idem"), testTxPwd)
	if code != 400 || resp.Error.Code != "INSUFFICIENT_BALANCE" {
		t.Fatalf("insufficient code=%d err=%+v, want 400/INSUFFICIENT_BALANCE", code, resp.Error)
	}
	ba, _ := balance(t, a)
	bb, _ := balance(t, b)
	if ba != 100 || bb != 0 {
		t.Fatalf("balances a=%d b=%d, want 100/0", ba, bb)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_txn WHERE user_id=$1 AND type=18`, a).Scan(&n); err != nil || n != 0 {
		t.Fatalf("transfer txn count=%d err=%v, want 0", n, err)
	}
	var status int
	var fcode string
	if err := testDB.QueryRow(`SELECT status, failure_code FROM transfer_order WHERE sender_id=$1 AND status=2 ORDER BY created_at DESC LIMIT 1`, a).Scan(&status, &fcode); err != nil {
		t.Fatalf("failed audit row missing: %v", err)
	}
	if status != TransferFailed || fcode != FailureInsufficientBalance {
		t.Fatalf("failed row status=%d failure=%s, want 2/INSUFFICIENT_BALANCE", status, fcode)
	}
}

// 68 错密码：403 TX_PASSWORD_ERROR、余额不动、无订单。
func Test68TransferWrongPassword(t *testing.T) {
	a := uniq("t68a")
	b := uniq("t68b")
	admitUser(t, a)
	fundUser(t, a, 5000)
	mustWallet(t, b)
	code, resp := sendTransferCall(t, a, b, 1000, uniq("idem"), "000000")
	if code != 403 || resp.Error.Code != "TX_PASSWORD_ERROR" {
		t.Fatalf("wrong pwd code=%d err=%+v, want 403/TX_PASSWORD_ERROR", code, resp.Error)
	}
	ba, _ := balance(t, a)
	if ba != 5000 {
		t.Fatalf("balance=%d, want 5000", ba)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM transfer_order WHERE sender_id=$1`, a).Scan(&n); err != nil || n != 0 {
		t.Fatalf("transfer order count=%d err=%v, want 0", n, err)
	}
}

// 69 转出方未完成准入引导：无方式/有方式无密码均 403 ADMISSION_NOT_SATISFIED、不扣款；补齐后可转。
func Test69TransferAdmissionGuidance(t *testing.T) {
	a := uniq("t69a")
	b := uniq("t69b")
	mustWallet(t, a)
	fundUser(t, a, 5000)
	mustWallet(t, b)
	code, resp := sendTransferCall(t, a, b, 1000, uniq("idem"), testTxPwd)
	if code != 403 || resp.Error.Code != "ADMISSION_NOT_SATISFIED" {
		t.Fatalf("no-method code=%d err=%+v, want 403/ADMISSION_NOT_SATISFIED", code, resp.Error)
	}
	addMethodOK(t, a, MethodAlipay, "Test User", "testuser@alipay.com", nil)
	code, resp = sendTransferCall(t, a, b, 1000, uniq("idem"), testTxPwd)
	if code != 403 || resp.Error.Code != "ADMISSION_NOT_SATISFIED" {
		t.Fatalf("no-password code=%d err=%+v, want 403/ADMISSION_NOT_SATISFIED", code, resp.Error)
	}
	ba, _ := balance(t, a)
	if ba != 5000 {
		t.Fatalf("balance after guided rejects=%d, want 5000", ba)
	}
	setTxPwd(t, a, testTxPwd)
	o := sendTransferOK(t, a, b, 1000, uniq("idem"))
	if o.Status != TransferSuccess {
		t.Fatalf("post-admission transfer status=%d", o.Status)
	}
}

// 70 自转拒绝：400 TRANSFER_SELF、余额不动、无订单。
func Test70TransferSelfRejected(t *testing.T) {
	a := uniq("t70a")
	admitUser(t, a)
	fundUser(t, a, 5000)
	code, resp := sendTransferCall(t, a, a, 1000, uniq("idem"), testTxPwd)
	if code != 400 || resp.Error.Code != "TRANSFER_SELF" {
		t.Fatalf("self code=%d err=%+v, want 400/TRANSFER_SELF", code, resp.Error)
	}
	ba, _ := balance(t, a)
	if ba != 5000 {
		t.Fatalf("balance=%d, want 5000", ba)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM transfer_order WHERE sender_id=$1`, a).Scan(&n); err != nil || n != 0 {
		t.Fatalf("transfer order count=%d err=%v, want 0", n, err)
	}
}

// 71 重复提交幂等：同一幂等键两次返回同一订单、只扣一次。
func Test71TransferIdempotent(t *testing.T) {
	a := uniq("t71a")
	b := uniq("t71b")
	admitUser(t, a)
	fundUser(t, a, 10000)
	mustWallet(t, b)
	idem := uniq("idem")
	o1 := sendTransferOK(t, a, b, 3000, idem)
	o2 := sendTransferOK(t, a, b, 3000, idem)
	if o1.OrderID != o2.OrderID || o1.TxnID == nil || o2.TxnID == nil || *o1.TxnID != *o2.TxnID {
		t.Fatalf("idempotent orders differ: %+v vs %+v", o1, o2)
	}
	ba, _ := balance(t, a)
	bb, _ := balance(t, b)
	if ba != 7000 || bb != 3000 {
		t.Fatalf("balances a=%d b=%d, want 7000/3000", ba, bb)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM transfer_order WHERE sender_id=$1 AND status=1`, a).Scan(&n); err != nil || n != 1 {
		t.Fatalf("success order count=%d err=%v, want 1", n, err)
	}
}

// 72 并发转账：20 并发各转 100（余额 1000），恰好 10 笔成功、最终余额 0、零超扣、收款方恰得 1000。
func Test72TransferConcurrent(t *testing.T) {
	a := uniq("t72a")
	b := uniq("t72b")
	admitUser(t, a)
	fundUser(t, a, 1000)
	mustWallet(t, b)
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 100; attempt++ {
				code, _ := sendTransferCall(t, a, b, 100, uniq("idem"), testTxPwd)
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
	ba, _ := balance(t, a)
	bb, _ := balance(t, b)
	if ba != 0 || bb != 1000 {
		t.Fatalf("balances a=%d b=%d, want 0/1000", ba, bb)
	}
	if ba < 0 {
		t.Fatalf("negative balance!")
	}
}

// 73 收款方自动建钱包：收款方从未建钱包仍可入账；收款方无准入（无方式无密码）不阻挡被动收款。
func Test73TransferReceiverAutoCreate(t *testing.T) {
	a := uniq("t73a")
	b := uniq("t73b") // 刻意不建钱包、不绑方式、不设密码
	admitUser(t, a)
	fundUser(t, a, 8000)
	o := sendTransferOK(t, a, b, 2500, uniq("idem"))
	if o.Status != TransferSuccess {
		t.Fatalf("transfer status=%d", o.Status)
	}
	bb, _ := balance(t, b) // 余额查询即证明钱包已自动建成
	if bb != 2500 {
		t.Fatalf("receiver balance=%d, want 2500", bb)
	}
	ba, _ := balance(t, a)
	if ba != 5500 {
		t.Fatalf("sender balance=%d, want 5500", ba)
	}
}

// 74 收款方冻结/注销钱包拒绝：400 RECEIVER_WALLET_ABNORMAL、双方余额不动、无成功订单。
func Test74TransferReceiverFrozenClosed(t *testing.T) {
	a := uniq("t74a")
	admitUser(t, a)
	fundUser(t, a, 9000)
	// 冻结收款方。
	bf := uniq("t74f")
	mustWallet(t, bf)
	if _, err := testDB.Exec(`UPDATE wallet_account SET status=1 WHERE user_id=$1`, bf); err != nil {
		t.Fatal(err)
	}
	code, resp := sendTransferCall(t, a, bf, 1000, uniq("idem"), testTxPwd)
	if code != 400 || resp.Error.Code != "RECEIVER_WALLET_ABNORMAL" {
		t.Fatalf("frozen code=%d err=%+v, want 400/RECEIVER_WALLET_ABNORMAL", code, resp.Error)
	}
	// 注销收款方。
	bc := uniq("t74c")
	mustWallet(t, bc)
	if _, err := testDB.Exec(`UPDATE wallet_account SET status=2 WHERE user_id=$1`, bc); err != nil {
		t.Fatal(err)
	}
	code, resp = sendTransferCall(t, a, bc, 1000, uniq("idem"), testTxPwd)
	if code != 400 || resp.Error.Code != "RECEIVER_WALLET_ABNORMAL" {
		t.Fatalf("closed code=%d err=%+v, want 400/RECEIVER_WALLET_ABNORMAL", code, resp.Error)
	}
	ba, _ := balance(t, a)
	if ba != 9000 {
		t.Fatalf("sender balance=%d, want 9000", ba)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM transfer_order WHERE sender_id=$1 AND status=1`, a).Scan(&n); err != nil || n != 0 {
		t.Fatalf("success order count=%d err=%v, want 0", n, err)
	}
}

// 75 发出/收到列表与详情可见性：sent 含单、received 含单、陌生人 received 为空、第三方详情 403。
func Test75TransferListsAndDetail(t *testing.T) {
	a := uniq("t75a")
	b := uniq("t75b")
	c := uniq("t75c")
	admitUser(t, a)
	fundUser(t, a, 10000)
	mustWallet(t, b)
	mustWallet(t, c)
	o := sendTransferOK(t, a, b, 1234, uniq("idem"))
	code, resp := call(t, "GET", "/v1/transfers?role=sent", a, nil, false)
	if code != 200 {
		t.Fatalf("sent list code=%d", code)
	}
	var sent []TransferOrder
	_ = json.Unmarshal(resp.Data, &sent)
	found := false
	for _, x := range sent {
		if x.OrderID == o.OrderID {
			found = true
		}
	}
	if !found {
		t.Fatalf("sent list missing order %s", o.OrderID)
	}
	code, resp = call(t, "GET", "/v1/transfers?role=received", b, nil, false)
	if code != 200 {
		t.Fatalf("received list code=%d", code)
	}
	var recv []TransferOrder
	_ = json.Unmarshal(resp.Data, &recv)
	found = false
	for _, x := range recv {
		if x.OrderID == o.OrderID {
			found = true
		}
	}
	if !found {
		t.Fatalf("received list missing order %s", o.OrderID)
	}
	code, resp = call(t, "GET", "/v1/transfers?role=received", c, nil, false)
	if code != 200 {
		t.Fatalf("stranger received code=%d", code)
	}
	var stranger []TransferOrder
	_ = json.Unmarshal(resp.Data, &stranger)
	for _, x := range stranger {
		if x.OrderID == o.OrderID {
			t.Fatalf("stranger received list leaks order %s", o.OrderID)
		}
	}
	code, _ = call(t, "GET", "/v1/transfers/"+o.OrderID, c, nil, false)
	if code != 403 {
		t.Fatalf("third-party detail code=%d, want 403", code)
	}
}

// 76 异常回滚：收款方冻结时整笔回滚——无 type 18 txn、无该 txn 分录、双方余额不动、仅失败审计行。
func Test76TransferAbnormalRollback(t *testing.T) {
	a := uniq("t76a")
	b := uniq("t76b")
	admitUser(t, a)
	fundUser(t, a, 6000)
	mustWallet(t, b)
	if _, err := testDB.Exec(`UPDATE wallet_account SET status=1 WHERE user_id=$1`, b); err != nil {
		t.Fatal(err)
	}
	code, _ := sendTransferCall(t, a, b, 2000, uniq("idem"), testTxPwd)
	if code != 400 {
		t.Fatalf("frozen transfer code=%d, want 400", code)
	}
	ba, _ := balance(t, a)
	if ba != 6000 {
		t.Fatalf("sender balance=%d, want 6000", ba)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_txn WHERE user_id=$1 AND type=18`, a).Scan(&n); err != nil || n != 0 {
		t.Fatalf("transfer txn count=%d err=%v, want 0", n, err)
	}
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_ledger l JOIN wallet_txn x ON l.txn_id=x.txn_id WHERE x.user_id=$1 AND x.type=18`, a).Scan(&n); err != nil || n != 0 {
		t.Fatalf("transfer ledger count=%d err=%v, want 0", n, err)
	}
	var fcode string
	if err := testDB.QueryRow(`SELECT failure_code FROM transfer_order WHERE sender_id=$1 AND status=2 ORDER BY created_at DESC LIMIT 1`, a).Scan(&fcode); err != nil || fcode != FailureReceiverFrozen {
		t.Fatalf("failed row failure=%s err=%v, want RECEIVER_FROZEN", fcode, err)
	}
}

// 77 账务守恒：成功 + 失败尝试后对账 diff=0，全部转账 txn 分录合计均为 0。
func Test77TransferLedgerConservation(t *testing.T) {
	a := uniq("t77a")
	b := uniq("t77b")
	admitUser(t, a)
	fundUser(t, a, 10000)
	mustWallet(t, b)
	sendTransferOK(t, a, b, 3000, uniq("idem"))
	sendTransferCall(t, a, b, 99999, uniq("idem"), testTxPwd) // 余额不足失败，不落账
	rows, err := testDB.Query(`SELECT txn_id FROM wallet_txn WHERE user_id=$1 AND type=18`, a)
	if err != nil {
		t.Fatal(err)
	}
	var txns []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		txns = append(txns, id)
	}
	rows.Close()
	if len(txns) != 1 {
		t.Fatalf("transfer txn count=%d, want 1", len(txns))
	}
	for _, id := range txns {
		ledgerSumZero(t, id)
	}
	code, resp := call(t, "POST", "/v1/admin/reconcile", "", nil, true)
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
}

// 78 用户权限隔离：请求体 user_id 与 token 不一致 403；平台结算户为收款方 403；管理接口无密钥 401。
func Test78TransferPermissionIsolation(t *testing.T) {
	a := uniq("t78a")
	b := uniq("t78b")
	admitUser(t, a)
	fundUser(t, a, 5000)
	mustWallet(t, b)
	code, _ := call(t, "POST", "/v1/transfers", a, map[string]any{"user_id": b, "receiver_id": b, "amount_cents": 100, "tx_password": testTxPwd}, false)
	if code != 403 {
		t.Fatalf("body user mismatch code=%d, want 403", code)
	}
	code, resp := sendTransferCall(t, a, PlatformUserID, 100, uniq("idem"), testTxPwd)
	if code != 403 {
		t.Fatalf("platform receiver code=%d err=%+v, want 403", code, resp.Error)
	}
	o := sendTransferOK(t, a, b, 500, uniq("idem"))
	code, _ = call(t, "GET", "/v1/admin/transfers/"+o.OrderID, a, nil, false)
	if code != 401 {
		t.Fatalf("admin without secret code=%d, want 401", code)
	}
	ba, _ := balance(t, a)
	if ba != 4500 {
		t.Fatalf("balance=%d, want 4500 (only the legitimate transfer applied)", ba)
	}
}

// 79 金额校验：金额 0 / 负数 / 附言超 200 字均 400 且不扣款、无成功订单。
func Test79TransferAmountValidation(t *testing.T) {
	a := uniq("t79a")
	b := uniq("t79b")
	admitUser(t, a)
	fundUser(t, a, 5000)
	mustWallet(t, b)
	code, _ := sendTransferCall(t, a, b, 0, uniq("idem"), testTxPwd)
	if code != 400 {
		t.Fatalf("zero amount code=%d, want 400", code)
	}
	code, _ = sendTransferCall(t, a, b, -100, uniq("idem"), testTxPwd)
	if code != 400 {
		t.Fatalf("negative amount code=%d, want 400", code)
	}
	longRemark := ""
	for i := 0; i < 201; i++ {
		longRemark += "转"
	}
	code, _ = call(t, "POST", "/v1/transfers", a, map[string]any{"receiver_id": b, "amount_cents": 100, "remark": longRemark, "tx_password": testTxPwd}, false)
	if code != 400 {
		t.Fatalf("long remark code=%d, want 400", code)
	}
	ba, _ := balance(t, a)
	if ba != 5000 {
		t.Fatalf("balance=%d, want 5000", ba)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM transfer_order WHERE sender_id=$1 AND status=1`, a).Scan(&n); err != nil || n != 0 {
		t.Fatalf("success order count=%d err=%v, want 0", n, err)
	}
}

// 80 管理端全链路查询：详情含订单 + txn + 双方 2 条分录（合计为 0）；按转出方过滤的列表含单；无密钥 401。
func Test80TransferAdminQuery(t *testing.T) {
	a := uniq("t80a")
	b := uniq("t80b")
	admitUser(t, a)
	fundUser(t, a, 7000)
	mustWallet(t, b)
	o := sendTransferOK(t, a, b, 2000, uniq("idem"))
	code, resp := call(t, "GET", "/v1/admin/transfers/"+o.OrderID, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin detail code=%d err=%v", code, resp.Error)
	}
	var d adminTransferDetail
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Order == nil || d.Order.OrderID != o.OrderID || d.Txn == nil || d.Txn.Type != TxnTransfer {
		t.Fatalf("admin detail order/txn unexpected: %+v", d)
	}
	if len(d.Entries) != 2 {
		t.Fatalf("admin detail entries=%d, want 2", len(d.Entries))
	}
	var sum int64
	for _, e := range d.Entries {
		sum += signed(e.Direction, e.AmountCents)
	}
	if sum != 0 {
		t.Fatalf("admin detail entries signed sum=%d, want 0", sum)
	}
	code, resp = call(t, "GET", "/v1/admin/transfers?sender_id="+a, "", nil, true)
	if code != 200 {
		t.Fatalf("admin list code=%d", code)
	}
	var list []TransferOrder
	_ = json.Unmarshal(resp.Data, &list)
	found := false
	for _, x := range list {
		if x.OrderID == o.OrderID {
			found = true
		}
	}
	if !found {
		t.Fatalf("admin list missing order %s", o.OrderID)
	}
	code, _ = call(t, "GET", "/v1/admin/transfers?sender_id="+a, "", nil, false)
	if code != 401 {
		t.Fatalf("admin list without secret code=%d, want 401", code)
	}
}

// 81 冲正纠错：转出方凭原 txn 冲正（type 8）后双方余额恢复；接收方已花掉款项时冲正失败、原转账不动。
// 纠错只走冲正交易，绝不直接改余额（本测试全程只用公开接口，不直接 UPDATE 余额）。
func Test81TransferReversal(t *testing.T) {
	a := uniq("t81a")
	b := uniq("t81b")
	admitUser(t, a)
	fundUser(t, a, 10000)
	mustWallet(t, b)
	o := sendTransferOK(t, a, b, 4000, uniq("idem"))
	code, resp := call(t, "POST", "/v1/ledger/reverse", a, map[string]any{"original_txn_id": *o.TxnID, "idempotency_key": uniq("idem")}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("reverse code=%d err=%v", code, resp.Error)
	}
	var rev Txn
	_ = json.Unmarshal(resp.Data, &rev)
	if rev.Type != TxnReversal {
		t.Fatalf("reversal txn type=%d, want 8", rev.Type)
	}
	ledgerSumZero(t, rev.TxnID)
	ba, _ := balance(t, a)
	bb, _ := balance(t, b)
	if ba != 10000 || bb != 0 {
		t.Fatalf("after reversal balances a=%d b=%d, want 10000/0", ba, bb)
	}
	// 接收方已花掉款项：再次转账 3000，接收方支出 3000 后，转出方冲正须整体失败。
	o2 := sendTransferOK(t, a, b, 3000, uniq("idem"))
	code, _ = call(t, "POST", "/v1/ledger/expense", b, map[string]any{"amount_cents": 3000, "idempotency_key": uniq("idem")}, false)
	if code != 200 {
		t.Fatalf("receiver expense code=%d", code)
	}
	code, _ = call(t, "POST", "/v1/ledger/reverse", a, map[string]any{"original_txn_id": *o2.TxnID, "idempotency_key": uniq("idem")}, false)
	if code == 200 {
		t.Fatalf("reversal after receiver spent unexpectedly succeeded")
	}
	ba, _ = balance(t, a)
	bb, _ = balance(t, b)
	if ba != 7000 || bb != 0 {
		t.Fatalf("after failed reversal balances a=%d b=%d, want 7000/0", ba, bb)
	}
}
