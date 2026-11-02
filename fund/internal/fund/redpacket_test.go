package fund

// Phase 3 测试：好友红包（单聊红包）23–34。全部用 stubVerifier（token "imtok-<uid>"），
// 测试库与命令与 Phase 1/2 相同。过期用 SQL 直接把 expires_at 改到过去（测试手段，
// 否则需等 24 小时；生产过期由扫描端点与领取惰性退款执行）。

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

const testTxPwd = "123456"

// admitUser：建钱包 + 绑一个支付宝方式 + 设交易密码（达到准入 ok）。
func admitUser(t *testing.T, uid string) {
	t.Helper()
	mustWallet(t, uid)
	addMethodOK(t, uid, MethodAlipay, "Test User", "testuser@alipay.com", nil)
	setTxPwd(t, uid, testTxPwd)
}

// fundUser：测试注资（内部 income 接口；真实充值在 Phase 8，测试注资不走支付渠道）。
func fundUser(t *testing.T, uid string, amount int64) {
	t.Helper()
	code, resp := call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": amount, "idempotency_key": uniq("idem")}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("fund %s %d: code=%d err=%+v", uid, amount, code, resp.Error)
	}
}

func sendPacketCall(t *testing.T, sender, receiver string, amount int64, idem string) (int, apiResp) {
	t.Helper()
	body := map[string]any{"receiver_id": receiver, "amount_cents": amount, "greeting": "恭喜发财", "tx_password": testTxPwd}
	if idem != "" {
		body["idempotency_key"] = idem
	}
	return call(t, "POST", "/v1/redpackets", sender, body, false)
}

func sendPacketOK(t *testing.T, sender, receiver string, amount int64, idem string) Packet {
	t.Helper()
	code, resp := sendPacketCall(t, sender, receiver, amount, idem)
	if code != 200 || !resp.OK {
		t.Fatalf("send packet %s->%s %d: code=%d err=%+v", sender, receiver, amount, code, resp.Error)
	}
	var p Packet
	if err := json.Unmarshal(resp.Data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

type claimResp struct {
	Claim  *Claim  `json:"claim"`
	Packet *Packet `json:"packet"`
}

func claimPacketCall(t *testing.T, claimer, packetID string) (int, apiResp) {
	t.Helper()
	return call(t, "POST", "/v1/redpackets/"+packetID+"/claim", claimer, map[string]any{}, false)
}

func claimPacketOK(t *testing.T, claimer, packetID string) claimResp {
	t.Helper()
	code, resp := claimPacketCall(t, claimer, packetID)
	if code != 200 || !resp.OK {
		t.Fatalf("claim %s packet %s: code=%d err=%+v", claimer, packetID, code, resp.Error)
	}
	var out claimResp
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func packetDetail(t *testing.T, viewer, packetID string) (int, apiResp) {
	t.Helper()
	return call(t, "GET", "/v1/redpackets/"+packetID, viewer, nil, false)
}

func expirePacketCall(t *testing.T, packetID string) (int, apiResp) {
	t.Helper()
	return callAdminOp(t, "POST", "/v1/admin/redpackets/expire", "admin1", map[string]any{"packet_id": packetID})
}

func forceExpire(t *testing.T, packetID string) {
	t.Helper()
	if _, err := testDB.Exec(`UPDATE redpacket_packet SET expires_at = now() - interval '1 hour' WHERE packet_id=$1`, packetID); err != nil {
		t.Fatal(err)
	}
}

func packetRowCount(t *testing.T, packetID string) int {
	t.Helper()
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM redpacket_packet WHERE packet_id=$1`, packetID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func claimRowCount(t *testing.T, packetID string) int {
	t.Helper()
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM redpacket_claim WHERE packet_id=$1`, packetID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 23 发包扣款 + 领取入账 + 包状态流转
func Test23SendClaim(t *testing.T) {
	a, b := uniq("rp23a"), uniq("rp23b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	p := sendPacketOK(t, a, b, 5000, uniq("idem"))
	if p.Status != PacketActive || p.AmountCents != 5000 || p.SenderID != a || p.ReceiverID != b {
		t.Fatalf("packet mismatch: %+v", p)
	}
	ba, fa := balance(t, a)
	if ba != 5000 || fa != 5000 {
		t.Fatalf("after send A balance=%d frozen=%d, want 5000/5000", ba, fa)
	}
	out := claimPacketOK(t, b, p.PacketID)
	if out.Claim.AmountCents != 5000 || out.Claim.ClaimerID != b || out.Packet.Status != PacketClaimed {
		t.Fatalf("claim mismatch: %+v %+v", out.Claim, out.Packet)
	}
	ba, fa = balance(t, a)
	bb, fb := balance(t, b)
	if ba != 5000 || fa != 0 {
		t.Fatalf("after claim A balance=%d frozen=%d, want 5000/0", ba, fa)
	}
	if bb != 5000 || fb != 0 {
		t.Fatalf("after claim B balance=%d frozen=%d, want 5000/0", bb, fb)
	}
	// 发包与领取两笔账务分录合计均为 0
	var txnIDs []string
	rows, err := testDB.Query(`SELECT send_txn_id FROM redpacket_packet WHERE packet_id=$1 UNION ALL SELECT claim_txn_id FROM redpacket_packet WHERE packet_id=$1`, p.PacketID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		txnIDs = append(txnIDs, id)
	}
	rows.Close()
	if len(txnIDs) != 2 {
		t.Fatalf("want 2 packet txns, got %d", len(txnIDs))
	}
	for _, id := range txnIDs {
		ledgerSumZero(t, id)
	}
}

// 24 重复发送幂等：同一幂等键两次发送只建一包、只冻一次
func Test24DuplicateSend(t *testing.T) {
	a, b := uniq("rp24a"), uniq("rp24b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	idem := uniq("idem")
	p1 := sendPacketOK(t, a, b, 3000, idem)
	p2 := sendPacketOK(t, a, b, 3000, idem)
	if p1.PacketID != p2.PacketID {
		t.Fatalf("duplicate send gave different packets: %s vs %s", p1.PacketID, p2.PacketID)
	}
	if packetRowCount(t, p1.PacketID) != 1 {
		t.Fatalf("duplicate send created extra packet rows")
	}
	ba, fa := balance(t, a)
	if ba != 7000 || fa != 3000 {
		t.Fatalf("after duplicate send A balance=%d frozen=%d, want 7000/3000", ba, fa)
	}
}

// 25 重复领取拦截：同一领取人重领返回原记录；他人领已领包返回 409
func Test25DuplicateClaim(t *testing.T) {
	a, b, c := uniq("rp25a"), uniq("rp25b"), uniq("rp25c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 10000)
	p := sendPacketOK(t, a, b, 2000, uniq("idem"))
	c1 := claimPacketOK(t, b, p.PacketID)
	c2 := claimPacketOK(t, b, p.PacketID) // 幂等重放
	if c1.Claim.ClaimID != c2.Claim.ClaimID {
		t.Fatalf("replay claim gave different claim: %s vs %s", c1.Claim.ClaimID, c2.Claim.ClaimID)
	}
	code, resp := claimPacketCall(t, c, p.PacketID)
	if code != 409 || resp.Error.Code != "PACKET_ALREADY_CLAIMED" {
		t.Fatalf("other claimer claim: code=%d err=%+v, want 409 PACKET_ALREADY_CLAIMED", code, resp.Error)
	}
	if claimRowCount(t, p.PacketID) != 1 {
		t.Fatalf("claim rows != 1")
	}
	bb, _ := balance(t, b)
	if bb != 2000 {
		t.Fatalf("B balance=%d, want 2000 (no double credit)", bb)
	}
}

// 26 并发领取：同一领取人 20 并发（含 429 重试）最终收敛到同一条领取记录、只入账一次
func Test26ConcurrentClaim(t *testing.T) {
	a, b := uniq("rp26a"), uniq("rp26b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	p := sendPacketOK(t, a, b, 4000, uniq("idem"))
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimIDs := map[string]bool{}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 100; attempt++ {
				code, resp := claimPacketCall(t, b, p.PacketID)
				if code == 200 {
					var out claimResp
					if err := json.Unmarshal(resp.Data, &out); err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					claimIDs[out.Claim.ClaimID] = true
					mu.Unlock()
					return
				}
				if code != 429 {
					t.Errorf("concurrent claim unexpected code=%d err=%+v", code, resp.Error)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			t.Errorf("concurrent claim never succeeded after retries")
		}()
	}
	wg.Wait()
	if len(claimIDs) != 1 {
		t.Fatalf("distinct claim ids=%d, want 1", len(claimIDs))
	}
	if claimRowCount(t, p.PacketID) != 1 {
		t.Fatalf("claim rows != 1")
	}
	bb, _ := balance(t, b)
	if bb != 4000 {
		t.Fatalf("B balance=%d, want 4000 (exactly once)", bb)
	}
}

// 27 余额不足：发包失败、不建包、不扣款
func Test27InsufficientBalance(t *testing.T) {
	a, b := uniq("rp27a"), uniq("rp27b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 100)
	code, resp := sendPacketCall(t, a, b, 200, uniq("idem"))
	if code == 200 {
		t.Fatalf("oversend succeeded")
	}
	if resp.Error.Code != "BUSINESS_ERROR" {
		t.Fatalf("oversend err=%+v, want BUSINESS_ERROR", resp.Error)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM redpacket_packet WHERE sender_id=$1`, a).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("failed send created %d packet rows", n)
	}
	ba, fa := balance(t, a)
	if ba != 100 || fa != 0 {
		t.Fatalf("A balance=%d frozen=%d, want 100/0", ba, fa)
	}
}

// 28 未完成准入的发送引导：无方式 / 有方式无密码均 403 引导语义、不扣款；补齐后可发
func Test28AdmissionGuidanceSend(t *testing.T) {
	a, b := uniq("rp28a"), uniq("rp28b")
	mustWallet(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	// 无提现方式
	code, resp := sendPacketCall(t, a, b, 1000, uniq("idem"))
	if code != 403 || resp.Error.Code != "ADMISSION_NOT_SATISFIED" {
		t.Fatalf("no-method send: code=%d err=%+v, want 403 ADMISSION_NOT_SATISFIED", code, resp.Error)
	}
	// 有方式无密码
	addMethodOK(t, a, MethodAlipay, "Test User", "testuser@alipay.com", nil)
	code, resp = sendPacketCall(t, a, b, 1000, uniq("idem"))
	if code != 403 || resp.Error.Code != "ADMISSION_NOT_SATISFIED" {
		t.Fatalf("no-password send: code=%d err=%+v, want 403 ADMISSION_NOT_SATISFIED", code, resp.Error)
	}
	ba, fa := balance(t, a)
	if ba != 10000 || fa != 0 {
		t.Fatalf("guided send must not deduct: balance=%d frozen=%d", ba, fa)
	}
	// 补齐密码后可发；错密码仍拒绝
	setTxPwd(t, a, testTxPwd)
	code, _ = call(t, "POST", "/v1/redpackets", a, map[string]any{"receiver_id": b, "amount_cents": 1000, "tx_password": "000000"}, false)
	if code != 403 {
		t.Fatalf("wrong password send: code=%d, want 403", code)
	}
	sendPacketOK(t, a, b, 1000, uniq("idem"))
}

// 29 未完成准入的领取引导：未完成准入的领取人领包只返回引导、包状态不动、冻结不动；补齐后可领
func Test29AdmissionGuidanceClaim(t *testing.T) {
	a, b := uniq("rp29a"), uniq("rp29b")
	admitUser(t, a)
	mustWallet(t, b) // B 未绑方式、未设密码
	fundUser(t, a, 10000)
	p := sendPacketOK(t, a, b, 2500, uniq("idem"))
	code, resp := claimPacketCall(t, b, p.PacketID)
	if code != 403 || resp.Error.Code != "ADMISSION_NOT_SATISFIED" {
		t.Fatalf("unadmitted claim: code=%d err=%+v, want 403 ADMISSION_NOT_SATISFIED", code, resp.Error)
	}
	code, resp = packetDetail(t, a, p.PacketID)
	if code != 200 {
		t.Fatalf("detail code=%d", code)
	}
	var det struct {
		Packet Packet `json:"packet"`
	}
	if err := json.Unmarshal(resp.Data, &det); err != nil {
		t.Fatal(err)
	}
	if det.Packet.Status != PacketActive {
		t.Fatalf("packet status after guided claim=%d, want 0 (unchanged)", det.Packet.Status)
	}
	ba, fa := balance(t, a)
	if ba != 7500 || fa != 2500 {
		t.Fatalf("guided claim must not move funds: A balance=%d frozen=%d", ba, fa)
	}
	if claimRowCount(t, p.PacketID) != 0 {
		t.Fatalf("guided claim created claim rows")
	}
	// B 补齐准入后可领
	addMethodOK(t, b, MethodAlipay, "Test User", "testuser@alipay.com", nil)
	setTxPwd(t, b, testTxPwd)
	claimPacketOK(t, b, p.PacketID)
}

// 30 用户权限隔离：第三方看不到包详情、领不到包、查不到领取记录
func Test30PermissionIsolation(t *testing.T) {
	a, b, c := uniq("rp30a"), uniq("rp30b"), uniq("rp30c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 10000)
	p := sendPacketOK(t, a, b, 1500, uniq("idem"))
	code, resp := claimPacketCall(t, c, p.PacketID)
	if code != 403 || resp.Error.Code != "FORBIDDEN" {
		t.Fatalf("third-party claim: code=%d err=%+v, want 403 FORBIDDEN", code, resp.Error)
	}
	code, _ = packetDetail(t, c, p.PacketID)
	if code != 403 {
		t.Fatalf("third-party detail: code=%d, want 403", code)
	}
	code, _ = call(t, "GET", "/v1/redpackets/"+p.PacketID+"/claims", c, nil, false)
	if code != 403 {
		t.Fatalf("third-party claims: code=%d, want 403", code)
	}
	// 收发双方可见
	code, _ = packetDetail(t, a, p.PacketID)
	if code != 200 {
		t.Fatalf("sender detail: code=%d, want 200", code)
	}
	code, _ = packetDetail(t, b, p.PacketID)
	if code != 200 {
		t.Fatalf("receiver detail: code=%d, want 200", code)
	}
}

// 31 过期自动退款（扫描端点）：过期包经扫描退款，发送方可用恢复、冻结清零；事后领取报过期
func Test31ExpiryRefundViaScanner(t *testing.T) {
	a, b := uniq("rp31a"), uniq("rp31b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	p := sendPacketOK(t, a, b, 3500, uniq("idem"))
	forceExpire(t, p.PacketID)
	code, resp := expirePacketCall(t, p.PacketID)
	if code != 200 || !resp.OK {
		t.Fatalf("expire call: code=%d err=%+v", code, resp.Error)
	}
	var exp struct {
		ExpiredIDs []string `json:"expired_ids"`
	}
	if err := json.Unmarshal(resp.Data, &exp); err != nil {
		t.Fatal(err)
	}
	if len(exp.ExpiredIDs) != 1 || exp.ExpiredIDs[0] != p.PacketID {
		t.Fatalf("expired_ids=%v, want [%s]", exp.ExpiredIDs, p.PacketID)
	}
	ba, fa := balance(t, a)
	if ba != 10000 || fa != 0 {
		t.Fatalf("after refund A balance=%d frozen=%d, want 10000/0", ba, fa)
	}
	// 退款账务分录合计为 0
	var refundTxn string
	if err := testDB.QueryRow(`SELECT refund_txn_id FROM redpacket_packet WHERE packet_id=$1`, p.PacketID).Scan(&refundTxn); err != nil {
		t.Fatal(err)
	}
	ledgerSumZero(t, refundTxn)
	// 再次扫描：幂等，不重复退款
	code, resp = expirePacketCall(t, p.PacketID)
	if code != 200 {
		t.Fatalf("second expire: code=%d", code)
	}
	if err := json.Unmarshal(resp.Data, &exp); err != nil {
		t.Fatal(err)
	}
	if len(exp.ExpiredIDs) != 0 {
		t.Fatalf("second expire refunded again: %v", exp.ExpiredIDs)
	}
	ba, _ = balance(t, a)
	if ba != 10000 {
		t.Fatalf("double refund: A balance=%d", ba)
	}
	// 事后领取报过期
	code, resp = claimPacketCall(t, b, p.PacketID)
	if code != 409 || resp.Error.Code != "PACKET_EXPIRED" {
		t.Fatalf("claim after refund: code=%d err=%+v, want 409 PACKET_EXPIRED", code, resp.Error)
	}
}

// 32 领取时惰性过期：过期包被领取时先自动退款、再报过期，发送方资金恢复
func Test32ClaimAfterExpiryLazyRefund(t *testing.T) {
	a, b := uniq("rp32a"), uniq("rp32b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	p := sendPacketOK(t, a, b, 4500, uniq("idem"))
	forceExpire(t, p.PacketID)
	code, resp := claimPacketCall(t, b, p.PacketID)
	if code != 409 || resp.Error.Code != "PACKET_EXPIRED" {
		t.Fatalf("expired claim: code=%d err=%+v, want 409 PACKET_EXPIRED", code, resp.Error)
	}
	ba, fa := balance(t, a)
	if ba != 10000 || fa != 0 {
		t.Fatalf("lazy refund: A balance=%d frozen=%d, want 10000/0", ba, fa)
	}
	bb, _ := balance(t, b)
	if bb != 0 {
		t.Fatalf("lazy refund must not credit claimer: B balance=%d", bb)
	}
}

// 33 账务守恒：一次完整发包->领取与一次发包->过期退款后，对账 diff=0，全部红包分录合计为 0
func Test33LedgerConservation(t *testing.T) {
	a, b := uniq("rp33a"), uniq("rp33b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 20000)
	p1 := sendPacketOK(t, a, b, 6000, uniq("idem"))
	claimPacketOK(t, b, p1.PacketID)
	p2 := sendPacketOK(t, a, b, 7000, uniq("idem"))
	forceExpire(t, p2.PacketID)
	if code, _ := expirePacketCall(t, p2.PacketID); code != 200 {
		t.Fatalf("expire code=%d", code)
	}
	rows, err := testDB.Query(`SELECT txn_id FROM wallet_txn WHERE ref_type='redpacket' AND (user_id=$1 OR user_id=$2)`, a, b)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	// send+claim+send+refund = 4 笔红包交易；注资交易的 ref_type 为空，不在其中
	if len(ids) != 4 {
		t.Fatalf("redpacket txn count=%d, want 4", len(ids))
	}
	for _, id := range ids {
		ledgerSumZero(t, id)
	}
	code, resp := call(t, "POST", "/v1/admin/reconcile", "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("reconcile code=%d err=%+v", code, resp.Error)
	}
	var rec ReconcileResult
	if err := json.Unmarshal(resp.Data, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.DiffCents != 0 || rec.Status != 0 {
		t.Fatalf("reconcile diff=%d status=%d, want 0/0", rec.DiffCents, rec.Status)
	}
}

// 34 异常防护：冻结行被异常篡改（remaining 与包金额不一致）时领取失败、包状态与领取记录均不动
func Test34AbnormalFreezeGuard(t *testing.T) {
	a, b := uniq("rp34a"), uniq("rp34b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	p := sendPacketOK(t, a, b, 3000, uniq("idem"))
	var freezeID string
	if err := testDB.QueryRow(`SELECT freeze_id FROM redpacket_packet WHERE packet_id=$1`, p.PacketID).Scan(&freezeID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Exec(`UPDATE wallet_freeze SET remaining_cents=100 WHERE freeze_id=$1`, freezeID); err != nil {
		t.Fatal(err)
	}
	code, resp := claimPacketCall(t, b, p.PacketID)
	if code == 200 {
		t.Fatalf("claim with tampered freeze succeeded")
	}
	if resp.Error.Code == "PACKET_ALREADY_CLAIMED" || resp.Error.Code == "PACKET_EXPIRED" {
		t.Fatalf("tampered claim gave wrong code: %+v", resp.Error)
	}
	code, resp = packetDetail(t, a, p.PacketID)
	if code != 200 {
		t.Fatalf("detail code=%d", code)
	}
	var det struct {
		Packet Packet `json:"packet"`
	}
	if err := json.Unmarshal(resp.Data, &det); err != nil {
		t.Fatal(err)
	}
	if det.Packet.Status != PacketActive {
		t.Fatalf("packet status after failed claim=%d, want 0", det.Packet.Status)
	}
	if claimRowCount(t, p.PacketID) != 0 {
		t.Fatalf("failed claim created claim rows")
	}
	bb, _ := balance(t, b)
	if bb != 0 {
		t.Fatalf("failed claim credited claimer: B balance=%d", bb)
	}
}
