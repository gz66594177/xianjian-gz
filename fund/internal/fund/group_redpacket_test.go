package fund

// Phase 4 测试：群普通红包（定额均分）35–48。全部用 stubVerifier（token "imtok-<uid>"），
// 测试库与命令与 Phase 1–3 相同。过期用 SQL 直接把 expires_at 改到过去（测试手段，
// 否则需等 24 小时；生产过期由扫描端点与领取惰性退款执行）。

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type groupClaimResp struct {
	Claim  *GroupClaim  `json:"claim"`
	Packet *GroupPacket `json:"packet"`
}

func sendGroupPacketCall(t *testing.T, sender, groupID string, total int64, count int, members []string, idem string) (int, apiResp) {
	t.Helper()
	body := map[string]any{"group_id": groupID, "total_amount_cents": total, "total_count": count, "greeting": "群发红包", "tx_password": testTxPwd}
	if members != nil {
		body["member_ids"] = members
	}
	if idem != "" {
		body["idempotency_key"] = idem
	}
	return call(t, "POST", "/v1/group-redpackets", sender, body, false)
}

func sendGroupPacketOK(t *testing.T, sender, groupID string, total int64, count int, members []string, idem string) GroupPacket {
	t.Helper()
	code, resp := sendGroupPacketCall(t, sender, groupID, total, count, members, idem)
	if code != 200 || !resp.OK {
		t.Fatalf("send group packet %s->%s %d x %d: code=%d err=%+v", sender, groupID, total, count, code, resp.Error)
	}
	var p GroupPacket
	if err := json.Unmarshal(resp.Data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func claimGroupPacketCall(t *testing.T, claimer, packetID string) (int, apiResp) {
	t.Helper()
	return call(t, "POST", "/v1/group-redpackets/"+packetID+"/claim", claimer, map[string]any{}, false)
}

func claimGroupPacketOK(t *testing.T, claimer, packetID string) groupClaimResp {
	t.Helper()
	code, resp := claimGroupPacketCall(t, claimer, packetID)
	if code != 200 || !resp.OK {
		t.Fatalf("claim group %s packet %s: code=%d err=%+v", claimer, packetID, code, resp.Error)
	}
	var out groupClaimResp
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func groupDetail(t *testing.T, viewer, packetID string) (int, apiResp) {
	t.Helper()
	return call(t, "GET", "/v1/group-redpackets/"+packetID, viewer, nil, false)
}

func groupDetailOK(t *testing.T, viewer, packetID string) GroupPacket {
	t.Helper()
	code, resp := groupDetail(t, viewer, packetID)
	if code != 200 || !resp.OK {
		t.Fatalf("group detail %s %s: code=%d err=%+v", viewer, packetID, code, resp.Error)
	}
	var p GroupPacket
	if err := json.Unmarshal(resp.Data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func expireGroupPacketCall(t *testing.T, packetID string) (int, apiResp) {
	t.Helper()
	return callAdminOp(t, "POST", "/v1/admin/group-redpackets/expire", "admin1", map[string]any{"packet_id": packetID})
}

func forceExpireGroup(t *testing.T, packetID string) {
	t.Helper()
	if _, err := testDB.Exec(`UPDATE group_redpacket_packet SET expires_at = now() - interval '1 hour' WHERE packet_id=$1`, packetID); err != nil {
		t.Fatal(err)
	}
}

func groupShareAmounts(t *testing.T, packetID string) []int64 {
	t.Helper()
	rows, err := testDB.Query(`SELECT amount_cents FROM group_redpacket_share WHERE packet_id=$1 ORDER BY seq`, packetID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var amt int64
		if err := rows.Scan(&amt); err != nil {
			t.Fatal(err)
		}
		out = append(out, amt)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func groupClaimCount(t *testing.T, packetID string) int {
	t.Helper()
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM group_redpacket_claim WHERE packet_id=$1`, packetID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 35 建包扣款 + 预拆分总和校验（1000 分 3 份 -> 334/333/333）
func Test35GroupSendSplit(t *testing.T) {
	a := uniq("grp35a")
	admitUser(t, a)
	fundUser(t, a, 10000)
	members := []string{a, uniq("grp35b"), uniq("grp35c"), uniq("grp35d")}
	p := sendGroupPacketOK(t, a, "group35", 1000, 3, members, uniq("idem"))
	if p.Status != GroupPacketActive || p.TotalAmountCents != 1000 || p.TotalCount != 3 || p.RemainingCount != 3 || p.RemainingAmountCents != 1000 {
		t.Fatalf("group packet mismatch: %+v", p)
	}
	shares := groupShareAmounts(t, p.PacketID)
	if len(shares) != 3 || shares[0] != 334 || shares[1] != 333 || shares[2] != 333 {
		t.Fatalf("shares=%v, want [334 333 333]", shares)
	}
	var sum int64
	for _, s := range shares {
		sum += s
	}
	if sum != 1000 {
		t.Fatalf("shares sum=%d, want 1000", sum)
	}
	ba, fa := balance(t, a)
	if ba != 9000 || fa != 1000 {
		t.Fatalf("after send A balance=%d frozen=%d, want 9000/1000", ba, fa)
	}
}

// 36 均分余数规则（直接测 splitAverage + 接口非法参数）
func Test36GroupSplitRules(t *testing.T) {
	cases := []struct {
		total int64
		count int
		want  []int64
	}{
		{100, 3, []int64{34, 33, 33}},
		{101, 10, []int64{11, 10, 10, 10, 10, 10, 10, 10, 10, 10}},
		{5, 5, []int64{1, 1, 1, 1, 1}},
		{10000, 7, []int64{1429, 1429, 1429, 1429, 1428, 1428, 1428}},
	}
	for _, c := range cases {
		got := splitAverage(c.total, c.count)
		if len(got) != len(c.want) {
			t.Fatalf("splitAverage(%d,%d) len=%d, want %d", c.total, c.count, len(got), len(c.want))
		}
		var sum int64
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("splitAverage(%d,%d)=%v, want %v", c.total, c.count, got, c.want)
			}
			if got[i] < 1 {
				t.Fatalf("splitAverage(%d,%d) has share < 1: %v", c.total, c.count, got)
			}
			sum += got[i]
		}
		if sum != c.total {
			t.Fatalf("splitAverage(%d,%d) sum=%d, want %d", c.total, c.count, sum, c.total)
		}
	}
	// 接口非法参数：总额 < 份数、份数 0、总额 0、份数超上限均拒绝且不扣款
	a := uniq("grp36a")
	admitUser(t, a)
	fundUser(t, a, 10000)
	for _, tc := range []struct {
		total int64
		count int
	}{{
		3, 5,
	}, {
		100, 0,
	}, {
		0, 3,
	}, {
		100, 1001,
	}} {
		code, resp := sendGroupPacketCall(t, a, "group36", tc.total, tc.count, nil, uniq("idem"))
		if code == 200 {
			t.Fatalf("invalid send total=%d count=%d succeeded", tc.total, tc.count)
		}
		if resp.Error.Code != "BAD_REQUEST" {
			t.Fatalf("invalid send total=%d count=%d err=%+v, want BAD_REQUEST", tc.total, tc.count, resp.Error)
		}
	}
	ba, fa := balance(t, a)
	if ba != 10000 || fa != 0 {
		t.Fatalf("invalid sends moved funds: balance=%d frozen=%d", ba, fa)
	}
}

// 37 领取入账 + 进度实时更新 + 领取列表
func Test37GroupClaimProgress(t *testing.T) {
	a, b, c := uniq("grp37a"), uniq("grp37b"), uniq("grp37c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 10000)
	p := sendGroupPacketOK(t, a, "group37", 1000, 3, []string{a, b, c}, uniq("idem"))
	out1 := claimGroupPacketOK(t, b, p.PacketID)
	if out1.Claim.AmountCents != 334 {
		t.Fatalf("first claim amount=%d, want 334 (earliest share)", out1.Claim.AmountCents)
	}
	d := groupDetailOK(t, a, p.PacketID)
	if d.ClaimedCount != 1 || d.ClaimedAmountCents != 334 || d.RemainingCount != 2 || d.RemainingAmountCents != 666 || d.Status != GroupPacketActive {
		t.Fatalf("detail after 1 claim: %+v", d)
	}
	bb, _ := balance(t, b)
	if bb != 334 {
		t.Fatalf("B balance=%d, want 334", bb)
	}
	out2 := claimGroupPacketOK(t, c, p.PacketID)
	if out2.Claim.AmountCents != 333 {
		t.Fatalf("second claim amount=%d, want 333", out2.Claim.AmountCents)
	}
	d = groupDetailOK(t, a, p.PacketID)
	if d.ClaimedCount != 2 || d.ClaimedAmountCents != 667 || d.RemainingCount != 1 || d.RemainingAmountCents != 333 {
		t.Fatalf("detail after 2 claims: %+v", d)
	}
	code, resp := call(t, "GET", "/v1/group-redpackets/"+p.PacketID+"/claims", a, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("claims list code=%d err=%+v", code, resp.Error)
	}
	var claims []GroupClaim
	if err := json.Unmarshal(resp.Data, &claims); err != nil {
		t.Fatal(err)
	}
	if len(claims) != 2 || claims[0].ClaimerID != b || claims[1].ClaimerID != c {
		t.Fatalf("claims list=%+v, want [b c]", claims)
	}
}

// 38 重复领取拦截：同一领取人重领返回原记录、余额与计数不动
func Test38GroupDuplicateClaim(t *testing.T) {
	a, b := uniq("grp38a"), uniq("grp38b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	p := sendGroupPacketOK(t, a, "group38", 900, 3, []string{a, b}, uniq("idem"))
	c1 := claimGroupPacketOK(t, b, p.PacketID)
	c2 := claimGroupPacketOK(t, b, p.PacketID)
	if c1.Claim.ClaimID != c2.Claim.ClaimID {
		t.Fatalf("replay claim gave different claim: %s vs %s", c1.Claim.ClaimID, c2.Claim.ClaimID)
	}
	if groupClaimCount(t, p.PacketID) != 1 {
		t.Fatalf("claim rows != 1 after replay")
	}
	d := groupDetailOK(t, a, p.PacketID)
	if d.ClaimedCount != 1 {
		t.Fatalf("claimed_count=%d after replay, want 1", d.ClaimedCount)
	}
	bb, _ := balance(t, b)
	if bb != c1.Claim.AmountCents {
		t.Fatalf("B balance=%d after replay, want %d (no double credit)", bb, c1.Claim.AmountCents)
	}
}

// 39 并发领取：3 个名额、10 个并发领取人，恰好 3 人成功、金额总和等于已领总额、不超发
func Test39GroupConcurrentClaim(t *testing.T) {
	a := uniq("grp39a")
	admitUser(t, a)
	fundUser(t, a, 10000)
	claimers := make([]string, 10)
	members := []string{a}
	for i := range claimers {
		claimers[i] = uniq("grp39c")
		admitUser(t, claimers[i])
		members = append(members, claimers[i])
	}
	p := sendGroupPacketOK(t, a, "group39", 900, 3, members, uniq("idem"))
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := map[string]int64{}
	for _, cl := range claimers {
		wg.Add(1)
		go func(cl string) {
			defer wg.Done()
			for attempt := 0; attempt < 100; attempt++ {
				code, resp := claimGroupPacketCall(t, cl, p.PacketID)
				if code == 200 {
					var out groupClaimResp
					if err := json.Unmarshal(resp.Data, &out); err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					success[cl] = out.Claim.AmountCents
					mu.Unlock()
					return
				}
				if code == 409 && resp.Error.Code == "GROUP_PACKET_FULL" {
					return
				}
				if code != 429 {
					t.Errorf("concurrent group claim unexpected code=%d err=%+v", code, resp.Error)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			t.Errorf("concurrent group claim never resolved for %s", cl)
		}(cl)
	}
	wg.Wait()
	if len(success) != 3 {
		t.Fatalf("successful claimers=%d, want 3", len(success))
	}
	if groupClaimCount(t, p.PacketID) != 3 {
		t.Fatalf("claim rows != 3")
	}
	var sum int64
	for _, amt := range success {
		sum += amt
	}
	if sum != 900 {
		t.Fatalf("claimed sum=%d, want 900 (exactly the packet total)", sum)
	}
	d := groupDetailOK(t, a, p.PacketID)
	if d.Status != GroupPacketDone || d.ClaimedCount != 3 || d.RemainingCount != 0 || d.RemainingAmountCents != 0 {
		t.Fatalf("detail after concurrent claims: %+v", d)
	}
	_, fa := balance(t, a)
	if fa != 0 {
		t.Fatalf("sender frozen=%d after full claim, want 0", fa)
	}
}

// 40 领完状态：最后一份领完后包状态为已领完、冻结清零；新领取人报已领完
func Test40GroupFullyClaimed(t *testing.T) {
	a, b, c, d := uniq("grp40a"), uniq("grp40b"), uniq("grp40c"), uniq("grp40d")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	admitUser(t, d)
	fundUser(t, a, 10000)
	p := sendGroupPacketOK(t, a, "group40", 600, 2, []string{a, b, c, d}, uniq("idem"))
	claimGroupPacketOK(t, b, p.PacketID)
	claimGroupPacketOK(t, c, p.PacketID)
	det := groupDetailOK(t, a, p.PacketID)
	if det.Status != GroupPacketDone || det.ClaimedCount != 2 || det.ClaimedAmountCents != 600 {
		t.Fatalf("fully claimed detail: %+v", det)
	}
	_, fa := balance(t, a)
	if fa != 0 {
		t.Fatalf("sender frozen=%d after full claim, want 0", fa)
	}
	code, resp := claimGroupPacketCall(t, d, p.PacketID)
	if code != 409 || resp.Error.Code != "GROUP_PACKET_FULL" {
		t.Fatalf("claim after full: code=%d err=%+v, want 409 GROUP_PACKET_FULL", code, resp.Error)
	}
}

// 41 过期部分退款（扫描端点）：2/3 领取后过期，剩余退回发送方；二次扫描不重复退
func Test41GroupExpiryPartialRefund(t *testing.T) {
	a, b, c := uniq("grp41a"), uniq("grp41b"), uniq("grp41c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 10000)
	p := sendGroupPacketOK(t, a, "group41", 900, 3, []string{a, b, c}, uniq("idem"))
	claimGroupPacketOK(t, b, p.PacketID) // 领 300
	forceExpireGroup(t, p.PacketID)
	code, resp := expireGroupPacketCall(t, p.PacketID)
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
	if ba != 9700 || fa != 0 {
		t.Fatalf("after partial refund A balance=%d frozen=%d, want 9700/0", ba, fa)
	}
	bb, _ := balance(t, b)
	if bb != 300 {
		t.Fatalf("claimer B balance=%d after refund, want 300 (kept)", bb)
	}
	if groupClaimCount(t, p.PacketID) != 1 {
		t.Fatalf("refund must keep existing claims")
	}
	var refundTxn string
	if err := testDB.QueryRow(`SELECT refund_txn_id FROM group_redpacket_packet WHERE packet_id=$1`, p.PacketID).Scan(&refundTxn); err != nil {
		t.Fatal(err)
	}
	ledgerSumZero(t, refundTxn)
	code, resp = expireGroupPacketCall(t, p.PacketID)
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
	if ba != 9700 {
		t.Fatalf("double refund: A balance=%d", ba)
	}
}

// 42 领取时惰性过期：过期包被领取时先退剩余、再报过期，发送方资金恢复、领取人不入账
func Test42GroupExpiryLazyRefundOnClaim(t *testing.T) {
	a, b := uniq("grp42a"), uniq("grp42b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	p := sendGroupPacketOK(t, a, "group42", 800, 2, []string{a, b}, uniq("idem"))
	forceExpireGroup(t, p.PacketID)
	code, resp := claimGroupPacketCall(t, b, p.PacketID)
	if code != 409 || resp.Error.Code != "GROUP_PACKET_EXPIRED" {
		t.Fatalf("expired claim: code=%d err=%+v, want 409 GROUP_PACKET_EXPIRED", code, resp.Error)
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

// 43 未完成准入的引导：发送与领取均只返回引导、不动资金；补齐后可继续
func Test43GroupAdmissionGuidance(t *testing.T) {
	a, b := uniq("grp43a"), uniq("grp43b")
	mustWallet(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	code, resp := sendGroupPacketCall(t, a, "group43", 900, 3, nil, uniq("idem"))
	if code != 403 || resp.Error.Code != "ADMISSION_NOT_SATISFIED" {
		t.Fatalf("no-method send: code=%d err=%+v, want 403 ADMISSION_NOT_SATISFIED", code, resp.Error)
	}
	ba, fa := balance(t, a)
	if ba != 10000 || fa != 0 {
		t.Fatalf("guided send must not deduct: balance=%d frozen=%d", ba, fa)
	}
	// 补齐发送方准入后可发；领取方未完成准入时只引导
	addMethodOK(t, a, MethodAlipay, "Test User", "testuser@alipay.com", nil)
	setTxPwd(t, a, testTxPwd)
	p := sendGroupPacketOK(t, a, "group43", 900, 3, nil, uniq("idem"))
	// 未完成准入的领取人用新用户 d（只有钱包，无方式无密码）
	d := uniq("grp43d")
	mustWallet(t, d)
	code, resp = claimGroupPacketCall(t, d, p.PacketID)
	if code != 403 || resp.Error.Code != "ADMISSION_NOT_SATISFIED" {
		t.Fatalf("unadmitted claim: code=%d err=%+v, want 403 ADMISSION_NOT_SATISFIED", code, resp.Error)
	}
	det := groupDetailOK(t, a, p.PacketID)
	if det.Status != GroupPacketActive || det.ClaimedCount != 0 {
		t.Fatalf("guided claim changed packet: %+v", det)
	}
	ba, fa = balance(t, a)
	if ba != 9100 || fa != 900 {
		t.Fatalf("guided claim must not move funds: A balance=%d frozen=%d", ba, fa)
	}
	if groupClaimCount(t, p.PacketID) != 0 {
		t.Fatalf("guided claim created claim rows")
	}
	addMethodOK(t, d, MethodAlipay, "Test User", "testuser@alipay.com", nil)
	setTxPwd(t, d, testTxPwd)
	claimGroupPacketOK(t, d, p.PacketID)
}

// 44 账务守恒：一次完整发包->领完与一次发包->部分领取->过期退款后，对账 diff=0，全部群包分录合计为 0
func Test44GroupLedgerConservation(t *testing.T) {
	a, b, c := uniq("grp44a"), uniq("grp44b"), uniq("grp44c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 20000)
	p1 := sendGroupPacketOK(t, a, "group44", 600, 2, []string{a, b, c}, uniq("idem"))
	claimGroupPacketOK(t, b, p1.PacketID)
	claimGroupPacketOK(t, c, p1.PacketID)
	p2 := sendGroupPacketOK(t, a, "group44", 900, 3, []string{a, b, c}, uniq("idem"))
	claimGroupPacketOK(t, b, p2.PacketID)
	forceExpireGroup(t, p2.PacketID)
	if code, _ := expireGroupPacketCall(t, p2.PacketID); code != 200 {
		t.Fatalf("expire code=%d", code)
	}
	rows, err := testDB.Query(`SELECT txn_id FROM wallet_txn WHERE ref_type='group_redpacket' AND (user_id=$1 OR user_id=$2 OR user_id=$3)`, a, b, c)
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
	// p1: send + 2 claim = 3；p2: send + 1 claim + 1 refund = 3；合计 6 笔群包交易
	if len(ids) != 6 {
		t.Fatalf("group redpacket txn count=%d, want 6", len(ids))
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

// 45 权限隔离：receiver 集合外的人领不到、看不到；发送方可领自己的群包
func Test45GroupPermissionIsolation(t *testing.T) {
	a, b, c := uniq("grp45a"), uniq("grp45b"), uniq("grp45c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 10000)
	p := sendGroupPacketOK(t, a, "group45", 900, 3, []string{a, b}, uniq("idem"))
	code, resp := claimGroupPacketCall(t, c, p.PacketID)
	if code != 403 || resp.Error.Code != "NOT_GROUP_MEMBER" {
		t.Fatalf("non-member claim: code=%d err=%+v, want 403 NOT_GROUP_MEMBER", code, resp.Error)
	}
	code, _ = groupDetail(t, c, p.PacketID)
	if code != 403 {
		t.Fatalf("non-member detail: code=%d, want 403", code)
	}
	code, _ = call(t, "GET", "/v1/group-redpackets/"+p.PacketID+"/claims", c, nil, false)
	if code != 403 {
		t.Fatalf("non-member claims: code=%d, want 403", code)
	}
	// 集合内成员与发送方可见；发送方领自己的包：可用 += 份额、冻结 -= 份额
	code, _ = groupDetail(t, b, p.PacketID)
	if code != 200 {
		t.Fatalf("member detail: code=%d, want 200", code)
	}
	baBefore, faBefore := balance(t, a)
	self := claimGroupPacketOK(t, a, p.PacketID)
	baAfter, faAfter := balance(t, a)
	if baAfter-baBefore != self.Claim.AmountCents || faBefore-faAfter != self.Claim.AmountCents {
		t.Fatalf("sender self-claim moved wrong amounts: bal %d->%d frozen %d->%d share %d", baBefore, baAfter, faBefore, faAfter, self.Claim.AmountCents)
	}
}

// 46 重复发送幂等：同一幂等键两次发送只建一包、只冻一次、份数不变
func Test46GroupIdempotentSend(t *testing.T) {
	a := uniq("grp46a")
	admitUser(t, a)
	fundUser(t, a, 10000)
	idem := uniq("idem")
	p1 := sendGroupPacketOK(t, a, "group46", 1200, 4, nil, idem)
	p2 := sendGroupPacketOK(t, a, "group46", 1200, 4, nil, idem)
	if p1.PacketID != p2.PacketID {
		t.Fatalf("duplicate send gave different packets: %s vs %s", p1.PacketID, p2.PacketID)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM group_redpacket_packet WHERE packet_id=$1`, p1.PacketID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate send packet rows=%d err=%v", n, err)
	}
	if got := len(groupShareAmounts(t, p1.PacketID)); got != 4 {
		t.Fatalf("duplicate send shares=%d, want 4", got)
	}
	ba, fa := balance(t, a)
	if ba != 8800 || fa != 1200 {
		t.Fatalf("after duplicate send A balance=%d frozen=%d, want 8800/1200", ba, fa)
	}
}

// 47 异常防护：冻结行被异常篡改（remaining 与剩余金额不一致）时领取失败、包状态与领取记录均不动
func Test47GroupAbnormalFreezeGuard(t *testing.T) {
	a, b := uniq("grp47a"), uniq("grp47b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	p := sendGroupPacketOK(t, a, "group47", 900, 3, []string{a, b}, uniq("idem"))
	var freezeID string
	if err := testDB.QueryRow(`SELECT freeze_id FROM group_redpacket_packet WHERE packet_id=$1`, p.PacketID).Scan(&freezeID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Exec(`UPDATE wallet_freeze SET remaining_cents=100 WHERE freeze_id=$1`, freezeID); err != nil {
		t.Fatal(err)
	}
	code, resp := claimGroupPacketCall(t, b, p.PacketID)
	if code == 200 {
		t.Fatalf("claim with tampered freeze succeeded")
	}
	if resp.Error.Code == "GROUP_PACKET_FULL" || resp.Error.Code == "GROUP_PACKET_EXPIRED" {
		t.Fatalf("tampered claim gave wrong code: %+v", resp.Error)
	}
	det := groupDetailOK(t, a, p.PacketID)
	if det.Status != GroupPacketActive || det.ClaimedCount != 0 {
		t.Fatalf("packet changed after failed claim: %+v", det)
	}
	if groupClaimCount(t, p.PacketID) != 0 {
		t.Fatalf("failed claim created claim rows")
	}
	bb, _ := balance(t, b)
	if bb != 0 {
		t.Fatalf("failed claim credited claimer: B balance=%d", bb)
	}
}

// 48 发出/收到列表：发送方 sent 含包、领取方 received 含包、陌生人 received 为空
func Test48GroupListEndpoints(t *testing.T) {
	a, b, c := uniq("grp48a"), uniq("grp48b"), uniq("grp48c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 10000)
	p := sendGroupPacketOK(t, a, "group48", 600, 2, []string{a, b, c}, uniq("idem"))
	claimGroupPacketOK(t, b, p.PacketID)
	code, resp := call(t, "GET", "/v1/group-redpackets?role=sent", a, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("sent list code=%d err=%+v", code, resp.Error)
	}
	var sent []GroupPacket
	if err := json.Unmarshal(resp.Data, &sent); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, gp := range sent {
		if gp.PacketID == p.PacketID {
			found = true
		}
	}
	if !found {
		t.Fatalf("sent list missing packet %s", p.PacketID)
	}
	code, resp = call(t, "GET", "/v1/group-redpackets?role=received", b, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("received list code=%d err=%+v", code, resp.Error)
	}
	var received []GroupPacket
	if err := json.Unmarshal(resp.Data, &received); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, gp := range received {
		if gp.PacketID == p.PacketID {
			found = true
		}
	}
	if !found {
		t.Fatalf("received list missing packet %s for claimer", p.PacketID)
	}
	code, resp = call(t, "GET", "/v1/group-redpackets?role=received", c, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("stranger received list code=%d err=%+v", code, resp.Error)
	}
	var stranger []GroupPacket
	if err := json.Unmarshal(resp.Data, &stranger); err != nil {
		t.Fatal(err)
	}
	for _, gp := range stranger {
		if gp.PacketID == p.PacketID {
			t.Fatalf("stranger received list contains packet %s", p.PacketID)
		}
	}
}
