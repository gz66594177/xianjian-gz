-- 仙聊资金服务 Phase 3 迁移 0003 回滚：删除红包表（回滚即删表，与 0001/0002 回滚语义一致）。
BEGIN;
DROP TABLE IF EXISTS redpacket_claim;
DROP TABLE IF EXISTS redpacket_packet;
COMMIT;
