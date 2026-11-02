package fund

// Phase 5 测试：群拼手气红包（随机预拆分）49–65。全部用 stubVerifier（token "imtok-<uid>"），
// 测试库与命令与 Phase 1–4 相同。过期用 SQL 直接把 expires_at 改到过去（测试手段，
// 否则需等 24 小时；生产过期由扫描端点与领取惰性退款执行）。
// 注意：拼手气份金额是建包时 crypto/rand 预拆分并落表的随机值，测试不得假设具体份额，
// 只断言硬约束（每份 ≥ 1 分、总和严格等于总额、无负数、不超剩）与份行建包后不变。

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type luckyClaimResp struct {
	Claim  *LuckyClaim  `json:"claim"`
	Packet *LuckyPacket `json:"packet"`
}

func sendLuckyPacketCall(t *testing.T, sender, groupID string, total int64, count int, members []string, idem string) (int, apiResp) {
	t.Helper()
	body := map[string]any{"group_id": groupID, "total_amount_cents": total, "total_count": count, "greeting": "拼手气红包", "tx_password": testTxPwd}
	if members != nil {
		body["member_ids"] = members
	}
	if idem != "" {
		body["idempotency_key"] = idem
	}
	return call(t, "POST", "/v1/group-lucky-redpackets", sender, body, false)
}

func sendLuckyPacketOK(t *testing.T, sender, groupID string, total int64, count int, members []string, idem string) LuckyPacket {
	t.Helper()
	code, resp := sendLuckyPacketCall(t, sender, groupID, total, count, members, idem)
	if code != 200 || !resp.OK {
		t.Fatalf("send lucky packet %s->%s %d x %d: code=%d err=%+v", sender, groupID, total, count, code, resp.Error)
	}
	var p LuckyPacket
	if err := json.Unmarshal(resp.Data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func claimLuckyPacketCall(t *testing.T, claimer, packetID string) (int, apiResp) {
	t.Helper()
	return call(t, "POST", "/v1/group-lucky-redpackets/"+packetID+"/claim", claimer, map[string]any{}, false)
}

func claimLuckyPacketOK(t *testing.T, claimer, packetID string) luckyClaimResp {
	t.Helper()
	code, resp := claimLuckyPacketCall(t, claimer, packetID)
	if code != 200 || !resp.OK {
		t.Fatalf("claim lucky %s packet %s: code=%d err=%+v", claimer, packetID, code, resp.Error)
	}
	var out luckyClaimResp
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func luckyDetail(t *testing.T, viewer, packetID string) (int, apiResp) {
	t.Helper()
	return call(t, "GET", "/v1/group-lucky-redpackets/"+packetID, viewer, nil, false)
}

func luckyDetailOK(t *testing.T, viewer, packetID string) LuckyPacket {
	t.Helper()
	code, resp := luckyDetail(t, viewer, packetID)
	if code != 200 || !resp.OK {
		t.Fatalf("lucky detail %s %s: code=%d err=%+v", viewer, packetID, code, resp.Error)
	}
	var p LuckyPacket
	if err := json.Unmarshal(resp.Data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func expireLuckyPacketCall(t *testing.T, packetID string) (int, apiResp) {
	t.Helper()
	return callAdminOp(t, "POST", "/v1/admin/group-lucky-redpackets/expire", "admin1", map[string]any{"packet_id": packetID})
}

func forceExpireLucky(t *testing.T, packetID string) {
	t.Helper()
	if _, err := testDB.Exec(`UPDATE group_lucky_redpacket_packet SET expires_at = now() - interval '1 hour' WHERE packet_id=$1`, packetID); err != nil {
		t.Fatal(err)
	}
}

func luckyShareAmounts(t *testing.T, packetID string) []int64 {
	t.Helper()
	rows, err := testDB.Query(`SELECT amount_cents FROM group_lucky_redpacket_share WHERE packet_id=$1 ORDER BY seq`, packetID)
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

func luckyClaimCount(t *testing.T, packetID string) int {
	t.Helper()
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM group_lucky_redpacket_claim WHERE packet_id=$1`, packetID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func luckyClaimsOK(t *testing.T, viewer, packetID string) []LuckyClaim {
	t.Helper()
	code, resp := call(t, "GET", "/v1/group-lucky-redpackets/"+packetID+"/claims", viewer, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("lucky claims list code=%d err=%+v", code, resp.Error)
	}
	var claims []LuckyClaim
	if err := json.Unmarshal(resp.Data, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func luckyRankingOK(t *testing.T, viewer, packetID string) []LuckyRank {
	t.Helper()
	code, resp := call(t, "GET", "/v1/group-lucky-redpackets/"+packetID+"/ranking", viewer, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("lucky ranking code=%d err=%+v", code, resp.Error)
	}
	var ranking []LuckyRank
	if err := json.Unmarshal(resp.Data, &ranking); err != nil {
		t.Fatal(err)
	}
	return ranking
}

// 49 随机分配算法专项：1000 次拆分，每份 ≥ 1 分、总和严格等于总额、无负数、每份不超剩
// （份额 ≤ 当时剩余金额 − 其余份底线）；总额 = 份数时全为 1 分。
func Test49LuckySplitInvariants(t *testing.T) {
	cases := []struct {
		total int64
		count int
	}{
		{10000, 100},
		{100, 3},
		{101, 10},
		{1000, 7},
		{100000, 1000},
		{7, 7},
		{1, 1},
	}
	for _, c := range cases {
		for iter := 0; iter < 1000; iter++ {
			shares, err := splitLucky(c.total, c.count)
			if err != nil {
				t.Fatalf("splitLucky(%d,%d) iter %d: %v", c.total, c.count, iter, err)
			}
			if len(shares) != c.count {
				t.Fatalf("splitLucky(%d,%d) len=%d, want %d", c.total, c.count, len(shares), c.count)
			}
			var sum int64
			remaining := c.total
			for i, s := range shares {
				if s < 1 {
					t.Fatalf("splitLucky(%d,%d) share[%d]=%d < 1", c.total, c.count, i, s)
				}
				// 不超剩：本份之后须为其余份各留 ≥ 1 分。
				if s > remaining-int64(c.count-i-1) {
					t.Fatalf("splitLucky(%d,%d) share[%d]=%d exceeds remaining %d minus reserve", c.total, c.count, i, s, remaining)
				}
				sum += s
				remaining -= s
			}
			if sum != c.total {
				t.Fatalf("splitLucky(%d,%d) sum=%d, want %d", c.total, c.count, sum, c.total)
			}
			if remaining != 0 {
				t.Fatalf("splitLucky(%d,%d) leftover=%d, want 0", c.total, c.count, remaining)
			}
		}
	}
	// 总额 = 份数时全为 1 分（确定性：upper 被留底线压到 1）。
	for i := 0; i < 100; i++ {
		shares, err := splitLucky(5, 5)
		if err != nil {
			t.Fatal(err)
		}
		for j, s := range shares {
			if s != 1 {
				t.Fatalf("splitLucky(5,5) share[%d]=%d, want 1", j, s)
			}
		}
	}
	// 非法输入拒绝。
	if _, err := splitLucky(3, 5); err == nil {
		t.Fatal("splitLucky(3,5) should fail")
	}
	if _, err := splitLucky(100, 0); err == nil {
		t.Fatal("splitLucky(100,0) should fail")
	}
}

// 50 分布合理性抽查：首份金额硬上界 2*均值-1；500 次首份均值落在均值附近（宽容区间，防 flaky）。
func Test50LuckySplitDistribution(t *testing.T) {
	const runs = 500
	var firstSum int64
	for i := 0; i < runs; i++ {
		shares, err := splitLucky(10000, 100)
		if err != nil {
			t.Fatal(err)
		}
		if shares[0] < 1 || shares[0] > 199 {
			t.Fatalf("first share=%d, want in [1,199] (2*mean-1)", shares[0])
		}
		firstSum += shares[0]
	}
	mean := float64(firstSum) / runs
	if mean < 80 || mean > 120 {
		t.Fatalf("first-share mean=%.1f over %d runs, want in [80,120] (theory 100)", mean, runs)
	}
	// 随机性存在：500 次首份不全相等（否则算法退化为均分）。
	seen := map[int64]bool{}
	for i := 0; i < 500; i++ {
		shares, _ := splitLucky(10000, 100)
		seen[shares[0]] = true
	}
	if len(seen) < 10 {
		t.Fatalf("first-share distinct values=%d, want >= 10 (randomness missing)", len(seen))
	}
}

// 51 建包扣款 + 预拆分总和校验（份行即审计依据：建包时确定、求和等于总额）
func Test51LuckySendSplit(t *testing.T) {
	a := uniq("lky51a")
	admitUser(t, a)
	fundUser(t, a, 10000)
	members := []string{a, uniq("lky51b"), uniq("lky51c"), uniq("lky51d")}
	p := sendLuckyPacketOK(t, a, "group51", 1000, 3, members, uniq("idem"))
	if p.Status != LuckyPacketActive || p.TotalAmountCents != 1000 || p.TotalCount != 3 || p.RemainingCount != 3 || p.RemainingAmountCents != 1000 {
		t.Fatalf("lucky packet mismatch: %+v", p)
	}
	if p.BestClaimerID != nil || p.BestAmountCents != nil {
		t.Fatalf("unclaimed packet best must be null, got %+v", p)
	}
	shares := luckyShareAmounts(t, p.PacketID)
	if len(shares) != 3 {
		t.Fatalf("shares len=%d, want 3", len(shares))
	}
	var sum int64
	for _, s := range shares {
		if s < 1 {
			t.Fatalf("share=%d < 1", s)
		}
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

// 52 接口非法参数：总额 < 份数、份数 0、总额 0、份数超上限均拒绝且不扣款
func Test52LuckyInvalidParams(t *testing.T) {
	a := uniq("lky52a")
	admitUser(t, a)
	fundUser(t, a, 10000)
	for _, tc := range []struct {
		total int64
		count int
	}{{3, 5}, {100, 0}, {0, 3}, {100, 1001}} {
		code, resp := sendLuckyPacketCall(t, a, "group52", tc.total, tc.count, nil, uniq("idem"))
		if code == 200 {
			t.Fatalf("invalid lucky send total=%d count=%d succeeded", tc.total, tc.count)
		}
		if resp.Error.Code != "BAD_REQUEST" {
			t.Fatalf("invalid lucky send total=%d count=%d err=%+v, want BAD_REQUEST", tc.total, tc.count, resp.Error)
		}
	}
	ba, fa := balance(t, a)
	if ba != 10000 || fa != 0 {
		t.Fatalf("invalid sends moved funds: balance=%d frozen=%d", ba, fa)
	}
}

// 53 领取入账 + 进度实时更新 + 领取金额等于预拆分份序金额（份行建包后不变）
func Test53LuckyClaimProgress(t *testing.T) {
	a, b, c := uniq("lky53a"), uniq("lky53b"), uniq("lky53c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 10000)
	p := sendLuckyPacketOK(t, a, "group53", 1000, 3, []string{a, b, c}, uniq("idem"))
	sharesBefore := luckyShareAmounts(t, p.PacketID)
	out1 := claimLuckyPacketOK(t, b, p.PacketID)
	if out1.Claim.AmountCents != sharesBefore[0] {
		t.Fatalf("first claim amount=%d, want earliest share %d", out1.Claim.AmountCents, sharesBefore[0])
	}
	d := luckyDetailOK(t, a, p.PacketID)
	if d.ClaimedCount != 1 || d.ClaimedAmountCents != sharesBefore[0] || d.RemainingCount != 2 || d.RemainingAmountCents != 1000-sharesBefore[0] || d.Status != LuckyPacketActive {
		t.Fatalf("detail after 1 claim: %+v", d)
	}
	bb, _ := balance(t, b)
	if bb != sharesBefore[0] {
		t.Fatalf("B balance=%d, want %d", bb, sharesBefore[0])
	}
	out2 := claimLuckyPacketOK(t, c, p.PacketID)
	if out2.Claim.AmountCents != sharesBefore[1] {
		t.Fatalf("second claim amount=%d, want share %d", out2.Claim.AmountCents, sharesBefore[1])
	}
	d = luckyDetailOK(t, a, p.PacketID)
	if d.ClaimedCount != 2 || d.ClaimedAmountCents != sharesBefore[0]+sharesBefore[1] || d.RemainingCount != 1 || d.RemainingAmountCents != sharesBefore[2] {
		t.Fatalf("detail after 2 claims: %+v", d)
	}
	// 份行金额建包后不变（可审计性：领取只改份状态，不改份金额）。
	sharesAfter := luckyShareAmounts(t, p.PacketID)
	for i := range sharesBefore {
		if sharesBefore[i] != sharesAfter[i] {
			t.Fatalf("share[%d] changed after claims: %d -> %d", i, sharesBefore[i], sharesAfter[i])
		}
	}
	claims := luckyClaimsOK(t, a, p.PacketID)
	if len(claims) != 2 || claims[0].ClaimerID != b || claims[1].ClaimerID != c {
		t.Fatalf("claims list=%+v, want [b c]", claims)
	}
}

// 54 手气最佳计算与排名：并列规则单测（最早领取者胜）+ 接口端最佳/排名一致
func Test54LuckyBestAndRanking(t *testing.T) {
	// 并列规则单测：金额并列时最早领取者为手气最佳；同时间则 claim_id 小者胜。
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	claims := []LuckyClaim{
		{ClaimID: "c1", ClaimerID: "u1", AmountCents: 200, ClaimedAt: t0},
		{ClaimID: "c2", ClaimerID: "u2", AmountCents: 300, ClaimedAt: t0.Add(time.Minute)},
		{ClaimID: "c3", ClaimerID: "u3", AmountCents: 300, ClaimedAt: t0.Add(2 * time.Minute)},
		{ClaimID: "c4", ClaimerID: "u4", AmountCents: 50, ClaimedAt: t0.Add(3 * time.Minute)},
	}
	best := bestLuckyClaim(claims)
	if best == nil || best.ClaimerID != "u2" {
		t.Fatalf("bestLuckyClaim tie: got %+v, want u2 (earliest among 300)", best)
	}
	sameTime := []LuckyClaim{
		{ClaimID: "c9", ClaimerID: "u9", AmountCents: 300, ClaimedAt: t0},
		{ClaimID: "c8", ClaimerID: "u8", AmountCents: 300, ClaimedAt: t0},
	}
	if b := bestLuckyClaim(sameTime); b == nil || b.ClaimID != "c8" {
		t.Fatalf("bestLuckyClaim same-time tie: got %+v, want c8", b)
	}
	if bestLuckyClaim(nil) != nil {
		t.Fatal("bestLuckyClaim(nil) should be nil")
	}
	// 接口端：3 人领完，详情最佳 = 最大领取额，排名首位 = 最佳，排名金额降序、序号连续。
	a, b, c, d := uniq("lky54a"), uniq("lky54b"), uniq("lky54c"), uniq("lky54d")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	admitUser(t, d)
	fundUser(t, a, 10000)
	p := sendLuckyPacketOK(t, a, "group54", 1000, 3, []string{a, b, c, d}, uniq("idem"))
	claimLuckyPacketOK(t, b, p.PacketID)
	claimLuckyPacketOK(t, c, p.PacketID)
	claimLuckyPacketOK(t, d, p.PacketID)
	det := luckyDetailOK(t, a, p.PacketID)
	if det.Status != LuckyPacketDone || det.BestClaimerID == nil || det.BestAmountCents == nil {
		t.Fatalf("done packet must carry best: %+v", det)
	}
	ranking := luckyRankingOK(t, a, p.PacketID)
	if len(ranking) != 3 {
		t.Fatalf("ranking len=%d, want 3", len(ranking))
	}
	var maxAmt int64
	for i, r := range ranking {
		if r.Rank != i+1 {
			t.Fatalf("ranking[%d].Rank=%d, want %d", i, r.Rank, i+1)
		}
		if i > 0 && r.AmountCents > ranking[i-1].AmountCents {
			t.Fatalf("ranking not descending: %+v", ranking)
		}
		if r.AmountCents > maxAmt {
			maxAmt = r.AmountCents
		}
	}
	if *det.BestAmountCents != maxAmt || *det.BestClaimerID != ranking[0].ClaimerID || ranking[0].AmountCents != maxAmt {
		t.Fatalf("detail best (%s %d) != ranking head (%s %d) / max %d", *det.BestClaimerID, *det.BestAmountCents, ranking[0].ClaimerID, ranking[0].AmountCents, maxAmt)
	}
}

// 55 重复领取拦截：同一领取人重领返回原记录、余额与计数不动
func Test55LuckyDuplicateClaim(t *testing.T) {
	a, b := uniq("lky55a"), uniq("lky55b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	p := sendLuckyPacketOK(t, a, "group55", 900, 3, []string{a, b}, uniq("idem"))
	c1 := claimLuckyPacketOK(t, b, p.PacketID)
	c2 := claimLuckyPacketOK(t, b, p.PacketID)
	if c1.Claim.ClaimID != c2.Claim.ClaimID {
		t.Fatalf("replay claim gave different claim: %s vs %s", c1.Claim.ClaimID, c2.Claim.ClaimID)
	}
	if luckyClaimCount(t, p.PacketID) != 1 {
		t.Fatalf("claim rows != 1 after replay")
	}
	d := luckyDetailOK(t, a, p.PacketID)
	if d.ClaimedCount != 1 {
		t.Fatalf("claimed_count=%d after replay, want 1", d.ClaimedCount)
	}
	bb, _ := balance(t, b)
	if bb != c1.Claim.AmountCents {
		t.Fatalf("B balance=%d after replay, want %d (no double credit)", bb, c1.Claim.AmountCents)
	}
}

// 56 并发领取：3 个名额、10 个并发领取人，恰好 3 人成功、金额总和等于总额、不超发
func Test56LuckyConcurrentClaim(t *testing.T) {
	a := uniq("lky56a")
	admitUser(t, a)
	fundUser(t, a, 10000)
	claimers := make([]string, 10)
	members := []string{a}
	for i := range claimers {
		claimers[i] = uniq("lky56c")
		admitUser(t, claimers[i])
		members = append(members, claimers[i])
	}
	p := sendLuckyPacketOK(t, a, "group56", 900, 3, members, uniq("idem"))
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := map[string]int64{}
	for _, cl := range claimers {
		wg.Add(1)
		go func(cl string) {
			defer wg.Done()
			for attempt := 0; attempt < 100; attempt++ {
				code, resp := claimLuckyPacketCall(t, cl, p.PacketID)
				if code == 200 {
					var out luckyClaimResp
					if err := json.Unmarshal(resp.Data, &out); err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					success[cl] = out.Claim.AmountCents
					mu.Unlock()
					return
				}
				if code == 409 && resp.Error.Code == "LUCKY_PACKET_FULL" {
					return
				}
				if code != 429 {
					t.Errorf("concurrent lucky claim unexpected code=%d err=%+v", code, resp.Error)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			t.Errorf("concurrent lucky claim never resolved for %s", cl)
		}(cl)
	}
	wg.Wait()
	if len(success) != 3 {
		t.Fatalf("successful claimers=%d, want 3", len(success))
	}
	if luckyClaimCount(t, p.PacketID) != 3 {
		t.Fatalf("claim rows != 3")
	}
	var sum int64
	for _, amt := range success {
		sum += amt
	}
	if sum != 900 {
		t.Fatalf("claimed sum=%d, want 900 (exactly the packet total)", sum)
	}
	d := luckyDetailOK(t, a, p.PacketID)
	if d.Status != LuckyPacketDone || d.ClaimedCount != 3 || d.RemainingCount != 0 || d.RemainingAmountCents != 0 {
		t.Fatalf("detail after concurrent claims: %+v", d)
	}
	_, fa := balance(t, a)
	if fa != 0 {
		t.Fatalf("sender frozen=%d after full claim, want 0", fa)
	}
}

// 57 领完状态：最后一份领完后包状态为已领完、冻结清零；新领取人报已领完
func Test57LuckyFullyClaimed(t *testing.T) {
	a, b, c, d := uniq("lky57a"), uniq("lky57b"), uniq("lky57c"), uniq("lky57d")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	admitUser(t, d)
	fundUser(t, a, 10000)
	p := sendLuckyPacketOK(t, a, "group57", 600, 2, []string{a, b, c, d}, uniq("idem"))
	claimLuckyPacketOK(t, b, p.PacketID)
	claimLuckyPacketOK(t, c, p.PacketID)
	det := luckyDetailOK(t, a, p.PacketID)
	if det.Status != LuckyPacketDone || det.ClaimedCount != 2 || det.ClaimedAmountCents != 600 {
		t.Fatalf("fully claimed detail: %+v", det)
	}
	_, fa := balance(t, a)
	if fa != 0 {
		t.Fatalf("sender frozen=%d after full claim, want 0", fa)
	}
	code, resp := claimLuckyPacketCall(t, d, p.PacketID)
	if code != 409 || resp.Error.Code != "LUCKY_PACKET_FULL" {
		t.Fatalf("claim after full: code=%d err=%+v, want 409 LUCKY_PACKET_FULL", code, resp.Error)
	}
}

// 58 过期部分退款（扫描端点）：1/3 领取后过期，剩余退回发送方；二次扫描不重复退
func Test58LuckyExpiryPartialRefund(t *testing.T) {
	a, b, c := uniq("lky58a"), uniq("lky58b"), uniq("lky58c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 10000)
	p := sendLuckyPacketOK(t, a, "group58", 900, 3, []string{a, b, c}, uniq("idem"))
	got := claimLuckyPacketOK(t, b, p.PacketID)
	forceExpireLucky(t, p.PacketID)
	code, resp := expireLuckyPacketCall(t, p.PacketID)
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
	if ba != 10000-got.Claim.AmountCents || fa != 0 {
		t.Fatalf("after partial refund A balance=%d frozen=%d, want %d/0", ba, fa, 10000-got.Claim.AmountCents)
	}
	bb, _ := balance(t, b)
	if bb != got.Claim.AmountCents {
		t.Fatalf("claimer B balance=%d after refund, want %d (kept)", bb, got.Claim.AmountCents)
	}
	if luckyClaimCount(t, p.PacketID) != 1 {
		t.Fatalf("refund must keep existing claims")
	}
	var refundTxn string
	if err := testDB.QueryRow(`SELECT refund_txn_id FROM group_lucky_redpacket_packet WHERE packet_id=$1`, p.PacketID).Scan(&refundTxn); err != nil {
		t.Fatal(err)
	}
	ledgerSumZero(t, refundTxn)
	code, resp = expireLuckyPacketCall(t, p.PacketID)
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
	if ba != 10000-got.Claim.AmountCents {
		t.Fatalf("double refund: A balance=%d", ba)
	}
}

// 59 领取时惰性过期：过期包被领取时先退剩余、再报过期，发送方资金恢复、领取人不入账
func Test59LuckyExpiryLazyRefundOnClaim(t *testing.T) {
	a, b := uniq("lky59a"), uniq("lky59b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	p := sendLuckyPacketOK(t, a, "group59", 800, 2, []string{a, b}, uniq("idem"))
	forceExpireLucky(t, p.PacketID)
	code, resp := claimLuckyPacketCall(t, b, p.PacketID)
	if code != 409 || resp.Error.Code != "LUCKY_PACKET_EXPIRED" {
		t.Fatalf("expired claim: code=%d err=%+v, want 409 LUCKY_PACKET_EXPIRED", code, resp.Error)
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

// 60 未完成准入的引导：发送与领取均只返回引导、不动资金；补齐后可继续
func Test60LuckyAdmissionGuidance(t *testing.T) {
	a, b := uniq("lky60a"), uniq("lky60b")
	mustWallet(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	code, resp := sendLuckyPacketCall(t, a, "group60", 900, 3, nil, uniq("idem"))
	if code != 403 || resp.Error.Code != "ADMISSION_NOT_SATISFIED" {
		t.Fatalf("no-method send: code=%d err=%+v, want 403 ADMISSION_NOT_SATISFIED", code, resp.Error)
	}
	ba, fa := balance(t, a)
	if ba != 10000 || fa != 0 {
		t.Fatalf("guided send must not deduct: balance=%d frozen=%d", ba, fa)
	}
	addMethodOK(t, a, MethodAlipay, "Test User", "testuser@alipay.com", nil)
	setTxPwd(t, a, testTxPwd)
	p := sendLuckyPacketOK(t, a, "group60", 900, 3, nil, uniq("idem"))
	d := uniq("lky60d")
	mustWallet(t, d)
	code, resp = claimLuckyPacketCall(t, d, p.PacketID)
	if code != 403 || resp.Error.Code != "ADMISSION_NOT_SATISFIED" {
		t.Fatalf("unadmitted claim: code=%d err=%+v, want 403 ADMISSION_NOT_SATISFIED", code, resp.Error)
	}
	det := luckyDetailOK(t, a, p.PacketID)
	if det.Status != LuckyPacketActive || det.ClaimedCount != 0 {
		t.Fatalf("guided claim changed packet: %+v", det)
	}
	ba, fa = balance(t, a)
	if ba != 9100 || fa != 900 {
		t.Fatalf("guided claim must not move funds: A balance=%d frozen=%d", ba, fa)
	}
	if luckyClaimCount(t, p.PacketID) != 0 {
		t.Fatalf("guided claim created claim rows")
	}
	addMethodOK(t, d, MethodAlipay, "Test User", "testuser@alipay.com", nil)
	setTxPwd(t, d, testTxPwd)
	claimLuckyPacketOK(t, d, p.PacketID)
}

// 61 账务守恒：一次完整发包->领完与一次发包->部分领取->过期退款后，对账 diff=0，全部拼手气分录合计为 0
func Test61LuckyLedgerConservation(t *testing.T) {
	a, b, c := uniq("lky61a"), uniq("lky61b"), uniq("lky61c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 20000)
	p1 := sendLuckyPacketOK(t, a, "group61", 600, 2, []string{a, b, c}, uniq("idem"))
	claimLuckyPacketOK(t, b, p1.PacketID)
	claimLuckyPacketOK(t, c, p1.PacketID)
	p2 := sendLuckyPacketOK(t, a, "group61", 900, 3, []string{a, b, c}, uniq("idem"))
	claimLuckyPacketOK(t, b, p2.PacketID)
	forceExpireLucky(t, p2.PacketID)
	if code, _ := expireLuckyPacketCall(t, p2.PacketID); code != 200 {
		t.Fatalf("expire code=%d", code)
	}
	rows, err := testDB.Query(`SELECT txn_id FROM wallet_txn WHERE ref_type='group_lucky_redpacket' AND (user_id=$1 OR user_id=$2 OR user_id=$3)`, a, b, c)
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
	// p1: send + 2 claim = 3；p2: send + 1 claim + 1 refund = 3；合计 6 笔拼手气交易
	if len(ids) != 6 {
		t.Fatalf("lucky redpacket txn count=%d, want 6", len(ids))
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

// 62 权限隔离：receiver 集合外的人领不到、看不到（含排名）；发送方可领自己的包
func Test62LuckyPermissionIsolation(t *testing.T) {
	a, b, c := uniq("lky62a"), uniq("lky62b"), uniq("lky62c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 10000)
	p := sendLuckyPacketOK(t, a, "group62", 900, 3, []string{a, b}, uniq("idem"))
	code, resp := claimLuckyPacketCall(t, c, p.PacketID)
	if code != 403 || resp.Error.Code != "NOT_GROUP_MEMBER" {
		t.Fatalf("non-member claim: code=%d err=%+v, want 403 NOT_GROUP_MEMBER", code, resp.Error)
	}
	code, _ = luckyDetail(t, c, p.PacketID)
	if code != 403 {
		t.Fatalf("non-member detail: code=%d, want 403", code)
	}
	code, _ = call(t, "GET", "/v1/group-lucky-redpackets/"+p.PacketID+"/claims", c, nil, false)
	if code != 403 {
		t.Fatalf("non-member claims: code=%d, want 403", code)
	}
	code, _ = call(t, "GET", "/v1/group-lucky-redpackets/"+p.PacketID+"/ranking", c, nil, false)
	if code != 403 {
		t.Fatalf("non-member ranking: code=%d, want 403", code)
	}
	code, _ = luckyDetail(t, b, p.PacketID)
	if code != 200 {
		t.Fatalf("member detail: code=%d, want 200", code)
	}
	baBefore, faBefore := balance(t, a)
	self := claimLuckyPacketOK(t, a, p.PacketID)
	baAfter, faAfter := balance(t, a)
	if baAfter-baBefore != self.Claim.AmountCents || faBefore-faAfter != self.Claim.AmountCents {
		t.Fatalf("sender self-claim moved wrong amounts: bal %d->%d frozen %d->%d share %d", baBefore, baAfter, faBefore, faAfter, self.Claim.AmountCents)
	}
}

// 63 重复发送幂等：同一幂等键两次发送只建一包、只冻一次、份数不变
func Test63LuckyIdempotentSend(t *testing.T) {
	a := uniq("lky63a")
	admitUser(t, a)
	fundUser(t, a, 10000)
	idem := uniq("idem")
	p1 := sendLuckyPacketOK(t, a, "group63", 1200, 4, nil, idem)
	p2 := sendLuckyPacketOK(t, a, "group63", 1200, 4, nil, idem)
	if p1.PacketID != p2.PacketID {
		t.Fatalf("duplicate send gave different packets: %s vs %s", p1.PacketID, p2.PacketID)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM group_lucky_redpacket_packet WHERE packet_id=$1`, p1.PacketID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate send packet rows=%d err=%v", n, err)
	}
	if got := len(luckyShareAmounts(t, p1.PacketID)); got != 4 {
		t.Fatalf("duplicate send shares=%d, want 4", got)
	}
	ba, fa := balance(t, a)
	if ba != 8800 || fa != 1200 {
		t.Fatalf("after duplicate send A balance=%d frozen=%d, want 8800/1200", ba, fa)
	}
}

// 64 异常防护：冻结行被异常篡改（remaining 与剩余金额不一致）时领取与过期退款均失败、包状态与领取记录均不动
func Test64LuckyAbnormalFreezeGuard(t *testing.T) {
	a, b := uniq("lky64a"), uniq("lky64b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 10000)
	p := sendLuckyPacketOK(t, a, "group64", 900, 3, []string{a, b}, uniq("idem"))
	var freezeID string
	if err := testDB.QueryRow(`SELECT freeze_id FROM group_lucky_redpacket_packet WHERE packet_id=$1`, p.PacketID).Scan(&freezeID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Exec(`UPDATE wallet_freeze SET remaining_cents=100 WHERE freeze_id=$1`, freezeID); err != nil {
		t.Fatal(err)
	}
	code, resp := claimLuckyPacketCall(t, b, p.PacketID)
	if code == 200 {
		t.Fatalf("claim with tampered freeze succeeded")
	}
	if resp.Error.Code == "LUCKY_PACKET_FULL" || resp.Error.Code == "LUCKY_PACKET_EXPIRED" {
		t.Fatalf("tampered claim gave wrong code: %+v", resp.Error)
	}
	forceExpireLucky(t, p.PacketID)
	code, _ = expireLuckyPacketCall(t, p.PacketID)
	if code == 200 {
		t.Fatalf("expire with tampered freeze succeeded")
	}
	det := luckyDetailOK(t, a, p.PacketID)
	if det.Status != LuckyPacketActive || det.ClaimedCount != 0 {
		t.Fatalf("packet changed after failed claim/expire: %+v", det)
	}
	if luckyClaimCount(t, p.PacketID) != 0 {
		t.Fatalf("failed claim created claim rows")
	}
	bb, _ := balance(t, b)
	if bb != 0 {
		t.Fatalf("failed claim credited claimer: B balance=%d", bb)
	}
}

// 65 发出/收到列表：发送方 sent 含包、领取方 received 含包、陌生人 received 为空
func Test65LuckyListEndpoints(t *testing.T) {
	a, b, c := uniq("lky65a"), uniq("lky65b"), uniq("lky65c")
	admitUser(t, a)
	admitUser(t, b)
	admitUser(t, c)
	fundUser(t, a, 10000)
	p := sendLuckyPacketOK(t, a, "group65", 600, 2, []string{a, b, c}, uniq("idem"))
	claimLuckyPacketOK(t, b, p.PacketID)
	code, resp := call(t, "GET", "/v1/group-lucky-redpackets?role=sent", a, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("sent list code=%d err=%+v", code, resp.Error)
	}
	var sent []LuckyPacket
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
	code, resp = call(t, "GET", "/v1/group-lucky-redpackets?role=received", b, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("received list code=%d err=%+v", code, resp.Error)
	}
	var received []LuckyPacket
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
	code, resp = call(t, "GET", "/v1/group-lucky-redpackets?role=received", c, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("stranger received list code=%d err=%+v", code, resp.Error)
	}
	var stranger []LuckyPacket
	if err := json.Unmarshal(resp.Data, &stranger); err != nil {
		t.Fatal(err)
	}
	for _, gp := range stranger {
		if gp.PacketID == p.PacketID {
			t.Fatalf("stranger received list contains packet %s", p.PacketID)
		}
	}
}
