package io.openim.android.ouiconversation.ui.fragment;

import java.util.ArrayList;
import java.util.List;

/**
 * 内置 GIF 系列：大肥鱼（diwer/ai-biaoqingbao，240x240 轻量版）。
 * 来源与授权见交付报告：非商业 AI 辅助二创，CC BY-NC-SA 4.0 链条，
 * 逐图原始许可尚未完全核实，正式对外分发前须用户确认授权。
 * 第 1 轮只打包 24 张日常高频；第 2 轮按仓库自带分类再补 5 个主题分组、
 * 共 53 张，全部是同一来源、同一角色的主题分组，不是 5 个无关 IP。
 * 选择器按 Group 分组展示；“我的表情”（自定义）与内置各组严格分开。
 */
public class XlGifPack {

    public static class Item {
        public final String name;
        public final String asset;

        public Item(String name, String asset) {
            this.name = name;
            this.asset = asset;
        }
    }

    public static class Group {
        public final String title;
        public final List<Item> items;

        public Group(String title, List<Item> items) {
            this.title = title;
            this.items = items;
        }
    }

    public static List<Item> builtin() {
        List<Item> list = new ArrayList<>();
        // 打招呼
        list.add(new Item("早安", "xl_gif/dafeiyu/017.gif"));
        list.add(new Item("晚安", "xl_gif/dafeiyu/018.gif"));
        list.add(new Item("在吗", "xl_gif/dafeiyu/019.gif"));
        list.add(new Item("再见", "xl_gif/dafeiyu/012.gif"));
        // 日常回应
        list.add(new Item("谢谢", "xl_gif/dafeiyu/008.gif"));
        list.add(new Item("辛苦了", "xl_gif/dafeiyu/027.gif"));
        list.add(new Item("收到！", "xl_gif/dafeiyu/009.gif"));
        list.add(new Item("好的", "xl_gif/dafeiyu/024.gif"));
        list.add(new Item("没问题", "xl_gif/dafeiyu/023.gif"));
        list.add(new Item("点赞", "xl_gif/dafeiyu/003.gif"));
        // 情绪
        list.add(new Item("开心", "xl_gif/dafeiyu/002.gif"));
        list.add(new Item("哈哈哈", "xl_gif/dafeiyu/030.gif"));
        list.add(new Item("笑死", "xl_gif/dafeiyu/029.gif"));
        list.add(new Item("委屈", "xl_gif/dafeiyu/004.gif"));
        list.add(new Item("生气", "xl_gif/dafeiyu/005.gif"));
        list.add(new Item("对不起", "xl_gif/dafeiyu/014.gif"));
        list.add(new Item("抱抱", "xl_gif/dafeiyu/010.gif"));
        // 生活
        list.add(new Item("开饭啦", "xl_gif/dafeiyu/015.gif"));
        list.add(new Item("干饭人", "xl_gif/dafeiyu/074.gif"));
        list.add(new Item("下班啦", "xl_gif/dafeiyu/063.gif"));
        list.add(new Item("摸鱼中", "xl_gif/dafeiyu/064.gif"));
        list.add(new Item("加油", "xl_gif/dafeiyu/016.gif"));
        list.add(new Item("恭喜", "xl_gif/dafeiyu/094.gif"));
        list.add(new Item("发财啦", "xl_gif/dafeiyu/095.gif"));
        return list;
    }

    /** 第 2 轮新增：抽象反应（斗图怼人/围观），16 张。 */
    public static List<Item> abstractReactions() {
        List<Item> list = new ArrayList<>();
        list.add(new Item("绷不住了", "xl_gif/dafeiyu/031.gif"));
        list.add(new Item("离谱", "xl_gif/dafeiyu/032.gif"));
        list.add(new Item("真的假的", "xl_gif/dafeiyu/033.gif"));
        list.add(new Item("啊这", "xl_gif/dafeiyu/034.gif"));
        list.add(new Item("好家伙", "xl_gif/dafeiyu/035.gif"));
        list.add(new Item("我裂开了", "xl_gif/dafeiyu/036.gif"));
        list.add(new Item("麻了", "xl_gif/dafeiyu/037.gif"));
        list.add(new Item("破防了", "xl_gif/dafeiyu/038.gif"));
        list.add(new Item("红温了", "xl_gif/dafeiyu/039.gif"));
        list.add(new Item("那咋了", "xl_gif/dafeiyu/040.gif"));
        list.add(new Item("已老实", "xl_gif/dafeiyu/041.gif"));
        list.add(new Item("求放过", "xl_gif/dafeiyu/042.gif"));
        list.add(new Item("吃瓜", "xl_gif/dafeiyu/046.gif"));
        list.add(new Item("拿捏了", "xl_gif/dafeiyu/047.gif"));
        list.add(new Item("你礼貌吗", "xl_gif/dafeiyu/050.gif"));
        list.add(new Item("退退退", "xl_gif/dafeiyu/053.gif"));
        return list;
    }

    /** 第 2 轮新增：亲密陪伴（撒娇卖萌），8 张。 */
    public static List<Item> intimate() {
        List<Item> list = new ArrayList<>();
        list.add(new Item("想你了", "xl_gif/dafeiyu/054.gif"));
        list.add(new Item("贴贴", "xl_gif/dafeiyu/055.gif"));
        list.add(new Item("亲亲", "xl_gif/dafeiyu/056.gif"));
        list.add(new Item("爱你", "xl_gif/dafeiyu/057.gif"));
        list.add(new Item("摸摸头", "xl_gif/dafeiyu/058.gif"));
        list.add(new Item("别难过", "xl_gif/dafeiyu/059.gif"));
        list.add(new Item("有我在", "xl_gif/dafeiyu/060.gif"));
        list.add(new Item("给你花", "xl_gif/dafeiyu/061.gif"));
        return list;
    }

    /** 第 2 轮新增：打工摸鱼（干饭打工），13 张。 */
    public static List<Item> work() {
        List<Item> list = new ArrayList<>();
        list.add(new Item("上班了", "xl_gif/dafeiyu/062.gif"));
        list.add(new Item("赶工中", "xl_gif/dafeiyu/065.gif"));
        list.add(new Item("我尽力了", "xl_gif/dafeiyu/066.gif"));
        list.add(new Item("不想上班", "xl_gif/dafeiyu/067.gif"));
        list.add(new Item("开会中", "xl_gif/dafeiyu/068.gif"));
        list.add(new Item("我太难了", "xl_gif/dafeiyu/069.gif"));
        list.add(new Item("CPU烧了", "xl_gif/dafeiyu/070.gif"));
        list.add(new Item("救命", "xl_gif/dafeiyu/071.gif"));
        list.add(new Item("让我缓缓", "xl_gif/dafeiyu/072.gif"));
        list.add(new Item("充电中", "xl_gif/dafeiyu/073.gif"));
        list.add(new Item("先吃一口", "xl_gif/dafeiyu/075.gif"));
        list.add(new Item("咖啡续命", "xl_gif/dafeiyu/076.gif"));
        list.add(new Item("发工资啦", "xl_gif/dafeiyu/077.gif"));
        return list;
    }

    /** 第 2 轮新增：情绪状态，11 张。 */
    public static List<Item> moods() {
        List<Item> list = new ArrayList<>();
        list.add(new Item("无语", "xl_gif/dafeiyu/006.gif"));
        list.add(new Item("困了", "xl_gif/dafeiyu/007.gif"));
        list.add(new Item("震惊", "xl_gif/dafeiyu/011.gif"));
        list.add(new Item("喜欢", "xl_gif/dafeiyu/013.gif"));
        list.add(new Item("疑惑", "xl_gif/dafeiyu/078.gif"));
        list.add(new Item("嫌弃", "xl_gif/dafeiyu/079.gif"));
        list.add(new Item("紧张", "xl_gif/dafeiyu/080.gif"));
        list.add(new Item("期待", "xl_gif/dafeiyu/081.gif"));
        list.add(new Item("害羞", "xl_gif/dafeiyu/082.gif"));
        list.add(new Item("偷笑", "xl_gif/dafeiyu/083.gif"));
        list.add(new Item("得意", "xl_gif/dafeiyu/084.gif"));
        return list;
    }

    /** 第 2 轮新增：祝福庆祝（节日祝福），5 张。 */
    public static List<Item> blessings() {
        List<Item> list = new ArrayList<>();
        list.add(new Item("接好运", "xl_gif/dafeiyu/096.gif"));
        list.add(new Item("欧气满满", "xl_gif/dafeiyu/097.gif"));
        list.add(new Item("好运加一", "xl_gif/dafeiyu/098.gif"));
        list.add(new Item("生日快乐", "xl_gif/dafeiyu/099.gif"));
        list.add(new Item("周末快乐", "xl_gif/dafeiyu/100.gif"));
        return list;
    }

    /** 选择器按此顺序分组展示：原日常组在前，5 个新组在后。 */
    public static List<Group> groups() {
        List<Group> groups = new ArrayList<>();
        groups.add(new Group("大肥鱼 · 日常", builtin()));
        groups.add(new Group("大肥鱼 · 抽象反应", abstractReactions()));
        groups.add(new Group("大肥鱼 · 亲密陪伴", intimate()));
        groups.add(new Group("大肥鱼 · 打工摸鱼", work()));
        groups.add(new Group("大肥鱼 · 情绪状态", moods()));
        groups.add(new Group("大肥鱼 · 祝福庆祝", blessings()));
        return groups;
    }
}
