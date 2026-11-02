# RuoYi 面板配置（仙聊）

来源：2026-11-02 香港三 `/root/ruoyi-xianliao-migration-20261102` 迁移快照。
本目录只含配置，不含面板源码（Vue/Java 源码、编译产物、jar 包均不在内）。

## 文件

- `menu-migration.sql`：仙聊资金管理菜单（2100 起）与权限（`xianliao:fund:*`）的迁移 SQL。
- `sys_menu-before.csv`：迁移前菜单快照（含原 YiChat 菜单，中文有双重编码乱码，属快照原样）。
- `application.yml` / `application-druid.yml`：Spring 配置快照，口令与密钥类值已脱敏为 `REDACTED`。
- `settings.js`：前端设置快照（标题、主题等，无密钥）。
- `ports-before.txt`：迁移前端口映射快照。
- `ruoyi.service`：systemd 服务单元快照（仙聊时期）。

## 未包含

- 面板源码（Vue/Java）与编译产物：按要求不推。
- 数据 dump（api-dump.sql、ry-vue-dump.sql、tigase-dump.sql）：含用户数据与密码哈希，不宜公开。
