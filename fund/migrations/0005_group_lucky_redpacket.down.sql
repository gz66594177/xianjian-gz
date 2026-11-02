-- 仙聊资金服务 Phase 5 回滚 0005：仅删群拼手气红包 4 张表（须先于 0004 down 执行；执行前必须备份）。
BEGIN;

DROP TABLE IF EXISTS group_lucky_redpacket_member;
DROP TABLE IF EXISTS group_lucky_redpacket_claim;
DROP TABLE IF EXISTS group_lucky_redpacket_share;
DROP TABLE IF EXISTS group_lucky_redpacket_packet;

COMMIT;
