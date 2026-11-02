-- 仙聊资金服务 Phase 4 回滚 0004：仅删群普通红包 4 张表（须先于 0003 down 执行；执行前必须备份）。
BEGIN;

DROP TABLE IF EXISTS group_redpacket_member;
DROP TABLE IF EXISTS group_redpacket_claim;
DROP TABLE IF EXISTS group_redpacket_share;
DROP TABLE IF EXISTS group_redpacket_packet;

COMMIT;
