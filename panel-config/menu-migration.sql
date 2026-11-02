-- RuoYi panel YiChat -> Xianliao menu migration (snapshot: sys_menu-before.csv, ry-vue-dump.sql)
DELETE FROM sys_role_menu WHERE menu_id BETWEEN 2000 AND 2051;
DELETE FROM sys_menu WHERE menu_id BETWEEN 2000 AND 2051;
INSERT INTO sys_menu (menu_id, parent_id, menu_name, order_num, path, component, query, route_name, is_frame, is_cache, menu_type, visible, status, perms, icon, create_by, create_time, remark) VALUES
(2100, 0, "仙聊资金管理", 10, "xianliao", NULL, "", "", 1, 0, "M", "0", "0", NULL, "money", "admin", NOW(), "仙聊资金管理（原 YiChat 菜单顶部位置）"),
(2101, 2100, "资金总览", 1, "overview", "xianliao/FundPage", "", "", 1, 0, "C", "0", "0", "xianliao:fund:list", "#", "admin", NOW(), ""),
(2102, 2100, "提现审核", 2, "withdraw", "xianliao/FundPage", "", "", 1, 0, "C", "0", "0", "xianliao:fund:list", "#", "admin", NOW(), ""),
(2103, 2100, "充值订单", 3, "recharge", "xianliao/FundPage", "", "", 1, 0, "C", "0", "0", "xianliao:fund:list", "#", "admin", NOW(), ""),
(2104, 2100, "资金准入查询", 4, "admission", "xianliao/FundPage", "", "", 1, 0, "C", "0", "0", "xianliao:fund:list", "#", "admin", NOW(), ""),
(2105, 2100, "提现账户查询", 5, "methods", "xianliao/FundPage", "", "", 1, 0, "C", "0", "0", "xianliao:fund:list", "#", "admin", NOW(), ""),
(2106, 2100, "钱包查询", 6, "wallets", "xianliao/FundPage", "", "", 1, 0, "C", "0", "0", "xianliao:fund:list", "#", "admin", NOW(), ""),
(2107, 2100, "审计日志", 7, "audit", "xianliao/FundPage", "", "", 1, 0, "C", "0", "0", "xianliao:fund:list", "#", "admin", NOW(), ""),
(2108, 2100, "手动充值审核", 8, "manual-pending", "xianliao/FundPage", "", "", 1, 0, "C", "0", "0", "xianliao:fund:list", "#", "admin", NOW(), "待后端接口交付后接线"),
(2109, 2100, "网关充值审核", 9, "gateway-pending", "xianliao/FundPage", "", "", 1, 0, "C", "0", "0", "xianliao:fund:list", "#", "admin", NOW(), "待后端接口交付后接线"),
(2110, 2102, "资金审核操作", 1, "", "", "", "", 1, 0, "F", "0", "0", "xianliao:fund:edit", "#", "admin", NOW(), "");
INSERT INTO sys_role_menu (role_id, menu_id) VALUES
(1, 2100), (1, 2101), (1, 2102), (1, 2103), (1, 2104), (1, 2105), (1, 2106), (1, 2107), (1, 2108), (1, 2109), (1, 2110);
