-- 仙聊资金服务 Phase 6 回滚 0006：仅删转账订单表（须先于 0005 down 执行；执行前必须备份）。
BEGIN;

DROP TABLE IF EXISTS transfer_order;

COMMIT;
